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

// Global logger object
var logger log.FieldLogger

// Config struct representing the YAML structure
type Config struct {
	SpreadsheetID         string `yaml:"spreadsheet-id"`
	SheetName             string `yaml:"sheet-name"`
	SheetKPILastUpdateCol string `yaml:"sheet-kpi-last-update-col"`
	SheetKPINameCol       string `yaml:"sheet-kpi-name-col"`
	SheetDataStartCol     string `yaml:"sheet-data-start-col"`
	SheetDataDateRow      string `yaml:"sheet-data-date-row"`
	KPI                   []KPIs `yaml:"KPI"`
}

// The KPIs struct holds the array of KPIs
type KPIs struct {
	Title          string `yaml:"title"`
	SheetRow       string `yaml:"sheet-row"`
	KPICommand     string `yaml:"kpi-command"`
	KPICommandArgs string `yaml:"kpi-command-args"`
	JSONEndpoint   string `yaml:"json-endpoint"`
	JSONDataPicker string `yaml:"json-data-picker"`
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
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "fatal":
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

// findThisWeekInSheet calculates the Column letter for this week
func findThisWeekColumnLetter(cfg *Config, srv *sheets.Service, nowYearWeek string) string {
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
	return clmconv.Itoa(dataStartColNum + dateOffset)
}

// updateKPIGoogleSheet updates the Google Spreadsheet
func updateKPIGoogleSheet(cfg *Config, srv *sheets.Service) {

	// Construct the string matching this week ("YYYY-WW")
	tn := time.Now().UTC()
	year, week := tn.ISOWeek()
	nowYearWeek := fmt.Sprintf("%d-%02d", year, week)
	lastUpdateDate := time.Now().Format("2006-01-02")
	logger.Info("Current Week is ", nowYearWeek)

	// Calculate the Column letter for this week
	dataWeekColLetter := findThisWeekColumnLetter(cfg, srv, nowYearWeek)

	// Generic value holder
	var vr sheets.ValueRange
	vr.Values = make([][]interface{}, 1)

	for _, kpi := range cfg.KPI {

		ok, out := scrapeEndpoint(&kpi)
		if !ok {
			continue
		}

		// Write KPI title
		writeSheetCell(&kpi,
			"Setting KPI title",
			[]interface{}{kpi.Title},
			cfg.SheetName+"!"+
				cfg.SheetKPINameCol+kpi.SheetRow+":"+
				cfg.SheetKPINameCol+kpi.SheetRow,
			cfg, srv, &vr)

		// Write KPI value
		writeSheetCell(&kpi,
			"Setting KPI value",
			[]interface{}{out},
			cfg.SheetName+"!"+
				dataWeekColLetter+kpi.SheetRow+":"+
				dataWeekColLetter+kpi.SheetRow,
			cfg, srv, &vr)

		// Update the 'last updated' date
		writeSheetCell(&kpi,
			"Setting last updated date",
			[]interface{}{lastUpdateDate},
			cfg.SheetName+"!"+
				cfg.SheetKPILastUpdateCol+kpi.SheetRow+":"+
				cfg.SheetKPILastUpdateCol+kpi.SheetRow,
			cfg, srv, &vr)
	}
}

// scrapeEndpoint connects to an HTTP service and retrieves and matches a JSON encoded value
// or runs and use the return number from an external command
func scrapeEndpoint(kpi *KPIs) (bool, int) {
	//log.Printf("Debug[%s]: JSONEndpoint: %s\n", kpi.Title, kpi.JSONEndpoint)
	var out int
	// Run the Web scrape command (if defined)
	if len(kpi.JSONEndpoint) > 0 {

		out = scrapeToJSON(kpi.JSONEndpoint, kpi.JSONDataPicker)
		return true, out

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
		return true, out

	} else {

		logger.WithFields(log.Fields{
			"kpi": kpi.Title,
		}).Warning("No way to gather data")
		return false, -1

	}
}

// writeSheetCell takes a number of parameters and updates a sheet cell with a specified value
func writeSheetCell(kpi *KPIs, action string, value []interface{}, cell string, cfg *Config, srv *sheets.Service, vr *sheets.ValueRange) {

	logger.WithFields(log.Fields{
		"cell": cell, "kpi": kpi.Title,
	}).Info(action)

	vr.Values[0] = value
	_, err := srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
		cell, vr).ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		logger.WithFields(log.Fields{
			"kpi":         kpi.Title,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
			"sheet":       cell,
			"value":       value,
		}).Fatal("Writing to sheet")
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
