package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	sheets "google.golang.org/api/sheets/v4"
	yaml "gopkg.in/yaml.v2"
)

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
	} `yaml:"KPI"`
}

const (
	clientSecretFile = "secret.json"
	configYaml       = "config.yaml"
)

// parseConfigYaml, access like: cfg.SpreadsheetID and cfg.KPI[0].Title
func parseConfigYaml(configYaml string) *Config {
	f, err := os.Open(configYaml)
	checkError(err)
	defer f.Close()

	var cfg Config
	decoder := yaml.NewDecoder(f)
	checkError(decoder.Decode(&cfg))
	return &cfg
}

// checkError just abort if anything fails
func checkError(err error) {
	if err != nil {
		log.Fatalf("%s\n", err.Error())
	}
}

func connectToGoogleSheet(clientSecretFile string) (*sheets.Service, error) {
	data, err := ioutil.ReadFile(clientSecretFile)
	checkError(err)
	conf, err := google.JWTConfigFromJSON(data, sheets.SpreadsheetsScope)
	checkError(err)

	client := conf.Client(context.TODO())
	srv, err := sheets.New(client)
	return srv, err
}

func main() {
	cfg := parseConfigYaml(configYaml)

	srv, err := connectToGoogleSheet(clientSecretFile)
	checkError(err)

	updateKPIGoogleSheet(cfg, srv)
}

// updateKPIGoogleSheet updates the Google Spreadsheet
func updateKPIGoogleSheet(cfg *Config, srv *sheets.Service) {

	// Construct the string matching this week ("YYYY-WW")
	tn := time.Now().UTC()
	year, week := tn.ISOWeek()
	nowYearWeek := fmt.Sprintf("%d-%02d", year, week)
	lastUpdateDate := time.Now().Format("2006-01-02")
	//fmt.Printf("Updating for week: %s\n", nowYearWeek)

	// Find the right week for this run
	yearWeekRow := cfg.SheetName + "!" + cfg.SheetDataStartCol +
		cfg.SheetDataDateRow + ":" + cfg.SheetDataDateRow
	//fmt.Printf("Date row: %v\n", yearWeekRow)
	resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID,
		yearWeekRow).Do()
	checkError(err)

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
	dataStartColNum, err := Atoi(cfg.SheetDataStartCol)
	dataWeekColLetter := Itoa(dataStartColNum + dateOffset)

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

		// Run KPI colleting command
		cmd := exec.Command(kpi.KPICommand, kpi.KPICommandArgs)
		tmpOut, err := cmd.CombinedOutput()
		var out int
		if err != nil {
			log.Fatalf("cmd.Run() failed with %s\n", err)
		}
		fmt.Sscanf(string(tmpOut), "%d", &out)

		// Write KPI title
		cell := cfg.SheetName + "!" +
			cfg.SheetKPINameCol + kpi.SheetRow + ":" +
			cfg.SheetKPINameCol + kpi.SheetRow
		fmt.Printf("KPI %v: Setting cell '%v' to: '%s' (KPI title)\n",
			i+1, cell, kpi.Title)
		vr.Values[0] = []interface{}{kpi.Title}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("RAW").Do()
		if err != nil {
			log.Fatalf("Unable to write data to sheet: %v\n", err)
		}

		// Write KPI data
		cell = cfg.SheetName + "!" +
			dataWeekColLetter + kpi.SheetRow + ":" +
			dataWeekColLetter + kpi.SheetRow
		fmt.Printf("KPI %v: Setting cell '%v' to: %d (KPI value for week %s)\n",
			i+1, cell, out, nowYearWeek)
		vr.Values[0] = []interface{}{out}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("USER_ENTERED").Do()
		if err != nil {
			log.Fatalf("Unable to write data to sheet: %v\n", err)
		}

		// Update the 'last updated' date
		cell = cfg.SheetName + "!" +
			cfg.SheetKPILastUpdateCol + kpi.SheetRow + ":" +
			cfg.SheetKPILastUpdateCol + kpi.SheetRow
		fmt.Printf("KPI %v: Setting cell '%v' to: %s (last update)\n",
			i+1, cell, lastUpdateDate)
		vr.Values[0] = []interface{}{lastUpdateDate}
		_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
			cell, &vr).
			ValueInputOption("USER_ENTERED").Do()
		if err != nil {
			log.Fatalf("Unable to write data to sheet: %v\n", err)
		}

	}
}

// This is copied from https://github.com/takuoki/clmconv
// - Neither GO Modules or go dependencies can find takuoki/clmconv (why?)
var pow26tab = [...]int{1, 26, 676, 17576, 456976, 11881376}

func pow26(n int) int {
	if 0 <= n && n <= 5 {
		return pow26tab[n]
	}
	return pow26(n-1) * 26
}

// Atoi converts alphabet to number.
func Atoi(s string) (int, error) {
	if s == "" {
		return 0, errors.New("argument is empty string")
	}
	var r int
	for i, c := range s {
		if 'A' <= c && c <= 'Z' {
			r += (int(c) - 64) * pow26(len(s)-i-1)
		} else if 'a' <= c && c <= 'z' {
			r += (int(c) - 96) * pow26(len(s)-i-1)
		} else {
			return 0, errors.New("must not contain non-alphabetic characters")
		}
	}
	return r - 1, nil
}

// Itoa converts number to alphabet.
// If argument is negative, returns empty string.
func Itoa(i int) string {
	if i < 0 {
		return ""
	}
	var r []rune
	for c := 0; ; c++ {
		mod := i%pow26(c+1) + 1
		r = append([]rune{rune(mod/pow26(c) + 64)}, r...)
		i -= mod
		if i <= 0 {
			break
		}
	}
	return string(r)
}
