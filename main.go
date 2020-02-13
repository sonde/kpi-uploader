package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/takuoki/clmconv"
	"github.com/tidwall/gjson"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	sheets "google.golang.org/api/sheets/v4"
	yaml "gopkg.in/yaml.v2"
)

var logger log.FieldLogger

// Config struct representing the YAML structure
type Config struct {
	SpreadsheetID         string `yaml:"spreadsheet-id"`
	SheetName             string `yaml:"sheet-name"`
	SheetKPILastUpdateCol string `yaml:"sheet-kpi-last-update-col"`
	SheetKPINameCol       string `yaml:"sheet-kpi-name-col"`
	SheetDataStartCol     string `yaml:"sheet-data-start-col"`
	SheetDataDateRow      string `yaml:"sheet-data-date-row"`
	KPI                   []struct {
		Title          string `yaml:"title"`
		SheetRow       string `yaml:"sheet-row"`
		KPICommand     string `yaml:"kpi-command"`
		KPICommandArgs string `yaml:"kpi-command-args"`
		JSONEndpoint   string `yaml:"json-endpoint"`
		JSONDataPicker string `yaml:"json-data-picker"`
	} `yaml:"KPI"`
}

const (
	clientSecretFile = "secret.json"
	configYaml       = "config.yaml"
)

// parseConfigYaml, access like: cfg.SpreadsheetID and cfg.KPI[0].Title
func parseConfigYaml(configYaml string) *Config {
	f, err := os.Open(configYaml)
	if err != nil {
		logger.WithFields(log.Fields{
			"configFile": configYaml,
			"error":      err,
		}).Fatal("Opening YAML file")
	}
	defer f.Close()

	var cfg Config
	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		logger.WithFields(log.Fields{
			"configFile": configYaml,
			"error":      err,
		}).Fatal("Decoding YAML file")
	}

	return &cfg
}

func connectToGoogleSheet(clientSecretFile string, cfg Config) *sheets.Service {
	data, err := ioutil.ReadFile(clientSecretFile)
	if err != nil {
		logger.WithFields(log.Fields{
			"secretFile":  clientSecretFile,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read secret file")
	}
	conf, err := google.JWTConfigFromJSON(data, sheets.SpreadsheetsScope)
	if err != nil {
		logger.WithFields(log.Fields{
			"sheetScope":  sheets.SpreadsheetsScope,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read Google sheet JSON config")
	}

	client := conf.Client(context.TODO())
	srv, err := sheets.New(client)
	if err != nil {
		logger.WithFields(log.Fields{
			"sheetScope":  sheets.SpreadsheetsScope,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Create sheet object")
	}
	return srv
}

func setupLogger() *log.FieldLogger {
	if os.Getenv("LOG_FORMAT") == "json" { // Fluentd field name conventions

		log.SetFormatter(&log.JSONFormatter{
			TimestampFormat: time.RFC3339Nano,
			FieldMap: log.FieldMap{
				log.FieldKeyTime:  "@timestamp",
				log.FieldKeyLevel: "level",
				log.FieldKeyMsg:   "message",
				log.FieldKeyFunc:  "caller",
			},
		})

		log.SetReportCaller(true)

	} else if os.Getenv("LOG_FORMAT") == "json_plain" { // Logrus field names
		log.SetFormatter(&log.JSONFormatter{})
	}
	if os.Getenv("LOG_STDOUT") == "true" {
		log.SetOutput(os.Stdout)
	}

	// Logrus has seven logging levels: Trace, Debug, Info, Warning, Error, Fatal and Panic.
	switch os.Getenv("LOG_LEVEL") { // LOG_LEVEL=warning
	case "INFO":
		log.SetLevel(log.InfoLevel)
	case "WARN":
		log.SetLevel(log.WarnLevel)
	case "FATAL":
		log.SetLevel(log.FatalLevel)
	default:
		log.SetLevel(log.FatalLevel)
	}

	var logger log.FieldLogger
	logger = log.WithFields(log.Fields{
		"@version": "1",
		"logger":   "kpi-uploader",
	})
	return &logger
}

func main() {

	logger = *setupLogger()

	logger.Info("Starting up")

	cfg := parseConfigYaml(configYaml)

	srv := connectToGoogleSheet(clientSecretFile, *cfg)

	updateKPIGoogleSheet(cfg, srv)

	logger.Info("Shutting down")
}

// updateKPIGoogleSheet updates the Google Spreadsheet
func updateKPIGoogleSheet(cfg *Config, srv *sheets.Service) {

	// Construct the string matching this week ("YYYY-WW")
	tn := time.Now().UTC()
	year, week := tn.ISOWeek()
	nowYearWeek := fmt.Sprintf("%d-%02d", year, week)
	lastUpdateDate := time.Now().Format("2006-01-02")
	logger.Info("Current Week is ", nowYearWeek)

	// Find the right week for this run
	yearWeekRow := cfg.SheetName + "!" + cfg.SheetDataStartCol +
		cfg.SheetDataDateRow + ":" + cfg.SheetDataDateRow
	resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID,
		yearWeekRow).Do()
	if err != nil {
		logger.WithFields(log.Fields{
			"cell":        yearWeekRow,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read data from sheet")
	}

	dateOffset := -1 // We must find the offset for this weeks column
	if len(resp.Values) == 0 {
		fmt.Println("No YYYY-WW dates found.")
	} else {
		for colCounter, row := range resp.Values[0] {
			//fmt.Printf(">> %v, %v\n", colCounter, row)
			if nowYearWeek == row {
				dateOffset = colCounter
				//fmt.Printf(">>>> %s == %s\n", nowYearWeek, row)
				break
			}
		}
	}
	if dateOffset == -1 {
		fmt.Printf("Could not find week %s\n", nowYearWeek)
		fmt.Printf("FIX: Add a new week column when a week is missing\n")
		os.Exit(1)
	}

	// Calculate the column letter (cfg.SheetDataStartCol + dateOffset)
	dataStartColNum, err := clmconv.Atoi(cfg.SheetDataStartCol)
	dataWeekColLetter := clmconv.Itoa(dataStartColNum + dateOffset)

	// Initialize the value setting vessel
	var vr sheets.ValueRange
	vr.Values = make([][]interface{}, 1)

	/* This is where we:
	 * 1. Re-write all the KPI Titles
	 * 2. Run the KPI value collecting commands (FIX: move to separate loop)
	 * 3. Write the KPI data
	 * 4. Update the last update column
	 */
	for i, kpi := range cfg.KPI {

		//log.Printf("Debug[%s]: JSONEndpoint: %s\n", kpi.Title, kpi.JSONEndpoint)
		var out int
		// Run the Web scrape command (if defined)
		if len(kpi.JSONEndpoint) > 0 {

			out = scrapeToJSON(kpi.JSONEndpoint, kpi.JSONDataPicker)

		} else if len(kpi.KPICommand) > 0 {

			// Run KPI colleting command
			cmd := exec.Command(kpi.KPICommand, kpi.KPICommandArgs)
			tmpOut, err := cmd.CombinedOutput()
			if err != nil {
				logger.WithFields(log.Fields{
					"kpi":     kpi.Title,
					"error":   err,
					"command": kpi.KPICommand + " " + kpi.KPICommandArgs,
				}).Fatal("Running external command")
			}
			fmt.Sscanf(string(tmpOut), "%d", &out) // Catch the result number

		} else {

			logger.WithFields(log.Fields{
				"kpi": kpi.Title,
			}).Warning("No way to gather data")
			continue

		}

		// Write KPI title
		cell := cfg.SheetName + "!" +
			cfg.SheetKPINameCol + kpi.SheetRow + ":" +
			cfg.SheetKPINameCol + kpi.SheetRow
		logger.WithFields(log.Fields{
			"kpiNum": i + 1, "cell": cell, "kpi": kpi.Title,
		}).Info("Setting KPI title")

		vr.Values[0] = []interface{}{kpi.Title}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("RAW").Do()
		if err != nil {
			logger.WithFields(log.Fields{
				"kpi":         kpi.Title,
				"error":       err,
				"spreadsheet": cfg.SpreadsheetID,
				"sheet":       cell,
				"value":       out,
			}).Fatal("Writing  to sheet")
		}

		// Write KPI value
		cell = cfg.SheetName + "!" +
			dataWeekColLetter + kpi.SheetRow + ":" +
			dataWeekColLetter + kpi.SheetRow
		logger.WithFields(log.Fields{
			"kpiNum": i + 1, "cell": cell, "value": out, "week": nowYearWeek,
		}).Info("Setting KPI value")

		vr.Values[0] = []interface{}{out}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("USER_ENTERED").Do()
		if err != nil {
			logger.WithFields(log.Fields{
				"kpi":         kpi.Title,
				"error":       err,
				"spreadsheet": cfg.SpreadsheetID,
				"sheet":       cell,
				"value":       out,
			}).Fatal("Writing value to sheet")
		}

		// Update the 'last updated' date
		cell = cfg.SheetName + "!" +
			cfg.SheetKPILastUpdateCol + kpi.SheetRow + ":" +
			cfg.SheetKPILastUpdateCol + kpi.SheetRow

		logger.WithFields(log.Fields{
			"kpiNum": i + 1, "cell": cell, "value": lastUpdateDate,
		}).Info("Setting last updated date")

		vr.Values[0] = []interface{}{lastUpdateDate}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("USER_ENTERED").Do()
		if err != nil {
			logger.WithFields(log.Fields{
				"kpi":         kpi.Title,
				"error":       err,
				"spreadsheet": cfg.SpreadsheetID,
				"sheet":       cell,
				"value":       out,
			}).Fatal("Unable to write data to sheet")
		}
	}
}

func scrapeToJSON(uri string, dataPicker string) int {
	if len(uri) == 0 {
		return -1
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Make request
	response, err := client.Get(uri)
	if err != nil {
		logger.Fatal(err)
	}
	defer response.Body.Close()
	//log.Printf("response: %s\n", response.Body)

	// Get the response body as a string
	dataInBytes, err := ioutil.ReadAll(response.Body)
	pageContent := string(dataInBytes)

	if err != nil {
		logger.Fatal(err)
	}

	value := gjson.Get(pageContent, dataPicker)

	var out int
	fmt.Sscanf(value.String(), "%d", &out)

	return out
}
