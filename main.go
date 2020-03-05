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

const (
	// Defaults
	clientSecretFileDefault = "secret.json"
	configYamlDefault       = "config.yaml"
)

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

// parseConfigYaml reads CONFIG_FILE or config.yaml,
// access config ie: cfg.SpreadsheetID and cfg.KPI[0].Title
func parseConfigYaml(configYamlDefault string) *Config {

	// Override the location and name of the config.yaml with CONFIG_FILE
	configYaml := configYamlDefault
	if os.Getenv("CONFIG_FILE") != "" {
		configYaml = os.Getenv("CONFIG_FILE")
	}

	f, err := os.Open(configYaml)
	if err != nil {
		logit.WithFields(log.Fields{
			"configFile": configYaml,
			"error":      err,
		}).Fatal("Opening YAML file")
	}
	defer func() { _ = f.Close() }()

	var cfg Config
	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		logit.WithFields(log.Fields{
			"configFile": configYaml,
			"error":      err,
		}).Fatal("Decoding YAML file")
	}

	return &cfg
}

func connectToGoogleSheet(clientSecretFileDefault string, cfg Config) *sheets.Service {

	// Override the location and name of the secret.json with SECRET_FILE
	clientSecretFile := clientSecretFileDefault
	if os.Getenv("SECRET_FILE") != "" {
		clientSecretFile = os.Getenv("SECRET_FILE")
	}

	data, err := ioutil.ReadFile(clientSecretFile)
	if err != nil {
		logit.WithFields(log.Fields{
			"secretFile":  clientSecretFile,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read secret file")
	}
	conf, err := google.JWTConfigFromJSON(data, sheets.SpreadsheetsScope)
	if err != nil {
		logit.WithFields(log.Fields{
			"sheetScope":  sheets.SpreadsheetsScope,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read Google sheet JSON config")
	}

	client := conf.Client(context.TODO())
	srv, err := sheets.New(client)
	if err != nil {
		logit.WithFields(log.Fields{
			"sheetScope":  sheets.SpreadsheetsScope,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Create sheet object")
	}
	return srv
}

func main() {

	logit.Info("Starting up")

	cfg := parseConfigYaml(configYamlDefault)

	go Serve(":8080", "/_/metrics", "/_/ready", "/_/alive", logit)

	srv := connectToGoogleSheet(clientSecretFileDefault, *cfg)

	updateKPIGoogleSheet(cfg, srv)

	// FIX: Should remember do disconnect from GoogleSheet?

	logit.Info("Shutting down")
}

// findThisWeekInSheet calculates the Column letter for this week
func findThisWeekColumnLetter(cfg *Config, srv *sheets.Service, nowYearWeek string) string {
	yearWeekRow := cfg.SheetName + "!" + cfg.SheetDataStartCol +
		cfg.SheetDataDateRow + ":" + cfg.SheetDataDateRow
	resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID,
		yearWeekRow).Do()
	if err != nil {
		logit.WithFields(log.Fields{
			"cell":        yearWeekRow,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read data from sheet")
	}

	dateOffset := -1 // We must find the offset for this weeks column
	if len(resp.Values) == 0 {
		logit.WithFields(log.Fields{
			"cell":        yearWeekRow,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("No YYYY-WW dates found in sheet")
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
		logit.WithFields(log.Fields{
			"cell":        yearWeekRow,
			"spreadsheet": cfg.SpreadsheetID,
		}).Debug("FIX: Add a new week column when a week is missing")
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
	logit.Info("Current Week is ", nowYearWeek)

	// Calculate the Column letter for this week
	dataWeekColLetter := findThisWeekColumnLetter(cfg, srv, nowYearWeek)

	// Variables for each state for gauge metrics
	//var valuesSynced, valuesCollision, valuesFailed int
	syncCount := map[int]int{
		errorCode["synced"]:    0,
		errorCode["collision"]: 0,
		errorCode["failed"]:    0,
	}

	// Generic value holder
	var vr sheets.ValueRange
	vr.Values = make([][]interface{}, 1)

	for _, kpi := range cfg.KPI {

		ok, out := scrapeEndpoint(&kpi)
		if !ok {
			continue
		}

		// Write KPI title
		// We should not overwrite a KPI title, only set it if
		// it is unset, we should also break off the update if
		// the KPI title is not matching.
		syncCount[writeSheetCell(&kpi,
			"Setting KPI title",
			[]interface{}{kpi.Title},
			cfg.SheetName+"!"+
				cfg.SheetKPINameCol+kpi.SheetRow+":"+
				cfg.SheetKPINameCol+kpi.SheetRow,
			cfg, srv, &vr, 0)]++

		// Write KPI value
		syncCount[writeSheetCell(&kpi,
			"Setting KPI value",
			[]interface{}{out},
			cfg.SheetName+"!"+
				dataWeekColLetter+kpi.SheetRow+":"+
				dataWeekColLetter+kpi.SheetRow,
			cfg, srv, &vr, 1)]++

		// Update the 'last updated' date
		syncCount[writeSheetCell(&kpi,
			"Setting last updated date",
			[]interface{}{lastUpdateDate},
			cfg.SheetName+"!"+
				cfg.SheetKPILastUpdateCol+kpi.SheetRow+":"+
				cfg.SheetKPILastUpdateCol+kpi.SheetRow,
			cfg, srv, &vr, 1)]++
	}

	// Set all the gauge metrics at the end to provide a consistent step
	//DataUploadedToSheet.WithLabelValues(syncStatusSynced).Set(float64(syncCount[errorCode["synced"]]))
	//DataUploadedToSheet.WithLabelValues(syncStatusFailed).Set(float64(syncCount[errorCode["failed"]]))
	//DataUploadedToSheet.WithLabelValues(syncStatusCollision).Set(float64(syncCount[errorCode["collision"]]))

}

// scrapeEndpoint connects to an HTTP service and retrieves and matches a JSON encoded value
// or runs and use the return number from an external command
func scrapeEndpoint(kpi *KPIs) (bool, int) {
	logit.Debug("Debug[", kpi.Title, "]: JSONEndpoint: '", kpi.JSONEndpoint, "'")
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
			logit.WithFields(log.Fields{
				"kpi":     kpi.Title,
				"error":   err,
				"command": kpi.KPICommand + " " + kpi.KPICommandArgs,
			}).Fatal("Running external command")
		}
		_, _ = fmt.Sscanf(string(tmpOut), "%d", &out) // Catch the result number
		return true, out

	} else {

		logit.WithFields(log.Fields{
			"kpi": kpi.Title,
		}).Warning("No way to gather data")
		return false, -1

	}
}

// writeSheetCell takes a number of parameters and updates a sheet cell with a specified value
func writeSheetCell(kpi *KPIs, action string, value []interface{},
	cell string, cfg *Config, srv *sheets.Service,
	vr *sheets.ValueRange, overwrite int) int {

	fmt.Printf("Debug 1: %v = %v\n", action, value[0])

	// Check if existing vakue is an empty value or if it is the
	// same value as we want to set.
	if overwrite == 0 {
		resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID,
			cell).Do()
		if err != nil {
			logit.WithFields(log.Fields{
				"cell":        cell,
				"error":       err,
				"spreadsheet": cfg.SpreadsheetID,
			}).Fatal("Read cell data from sheet")
		}

		// A value exists but is not the same as we got.
		if len(resp.Values) > 0 && resp.Values[0][0] != value[0] {
			logit.WithFields(log.Fields{
				"cell":        cell,
				"error":       err,
				"spreadsheet": cfg.SpreadsheetID,
				"cellValue":   resp.Values[0][0],
				"newValue":    value[0],
			}).Warning("Skip ", action)

			// fmt.Printf("Skip %s as %v != %v\n", action, resp.Values[0][0], value[0])
			return errorCode["collision"]
		}
	}
	logit.WithFields(log.Fields{
		"cell": cell, "kpi": kpi.Title,
	}).Info(action)

	vr.Values[0] = value
	_, err := srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
		cell, vr).ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		logit.WithFields(log.Fields{
			"kpi":         kpi.Title,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
			"sheet":       cell,
			"value":       value,
		}).Fatal("Writing to sheet")
		return errorCode["synced"]
	}
	return errorCode["failed"]
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
		logit.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	logit.Debug("Response body: %s\n", response.Body)

	// Get the response body as a string
	dataInBytes, err := ioutil.ReadAll(response.Body)
	pageContent := string(dataInBytes)

	if err != nil {
		logit.Fatal(err)
	}

	value := gjson.Get(pageContent, dataPicker)

	var out int
	_, _ = fmt.Sscanf(value.String(), "%d", &out)

	return out
}
