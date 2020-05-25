package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
	SpreadsheetID      string `yaml:"spreadsheet-id"`
	SheetName          string `yaml:"sheet-name"`
	SheetLastUpdateCol string `yaml:"sheet-last-update-col"`
	SheetKeyCol        string `yaml:"sheet-key-col"`
	SheetTopicRow      string `yaml:"sheet-topic-row"`
	SheetDataStartRow  string `yaml:"sheet-data-start-row"`
	CkecksPort         string `yaml:"ckecks-port"`
	ChecksPathMetrics  string `yaml:"checks-path-metrics"`
	ChecksPathReady    string `yaml:"checks-path-ready"`
	ChecksPathLive     string `yaml:"checks-path-live"`

	Datapoints []Datapoint `yaml:"datapoints"`
	KPI        []KPIs      `yaml:"KPI"` // Legacy actually, will be replaced over time
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

// The Datapoint struct holds the array of KPIs
// Specifying add-rows works best when prepolulating some extra rows with required cell functions
// with relevant cell functions etc copied in.
type Datapoint struct {
	Title     string `yaml:"title"`
	Command   string `yaml:"command"`
	Args      string `yaml:"args"`
	AddRows   string `yaml:"add-rows"`   // Specify this if you want to add non-existing rows
	SheetName string `yaml:"sheet-name"` // Override the default sheet name if you need to

	KeyCol   string `yaml:"key-col"`   // Alternate key column for this data type
	MatchAll string `yaml:"match-all"` // Alternate keys are often not unique keys

	Cell  string `yaml:"cell"`  // Optinal specification of a single cell
	Value string `yaml:"value"` // combined with a single value to f.i set an "Updating" message
}

// Stores the horizontal and vertical mapping, i.e:
// topicCache["some topic"] = clmconv.Itoa(dataStartColNum + colCounter) /* I.e "AZ" */
// keyCache["some key"] = sheetDataStartRow + rowCounter /* I.e 67 */
var topicCache map[string]string
var keyCache map[string]int
var keyCacheArr map[string][]int
var keyCacheMax int

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
	// Echo main config parameters
	logit.WithFields(log.Fields{
		"spreadsheet-id":        cfg.SpreadsheetID,
		"sheet-name":            cfg.SheetName,
		"sheet-last-update-col": cfg.SheetLastUpdateCol,
		"sheet-key-col":         cfg.SheetKeyCol,
		"sheet-topic-row":       cfg.SheetTopicRow,
		"sheet-data-start-row":  cfg.SheetDataStartRow,
		"ckecks-port":           cfg.CkecksPort,
		"checks-path-metrics":   cfg.ChecksPathMetrics,
		"checks-path-ready":     cfg.ChecksPathReady,
		"checks-path-live":      cfg.ChecksPathLive,
	}).Debug("Spreadsheet")

	// go Serve(":8080", "/_/metrics", "/_/ready", "/_/alive", logit)
	go Serve(cfg.CkecksPort, cfg.ChecksPathMetrics, cfg.ChecksPathReady,
		cfg.ChecksPathLive, logit)

	srv := connectToGoogleSheet(clientSecretFileDefault, *cfg)

	if cfg.KPI != nil {
		logit.Debug("Taking the KPI branch!") // Legacy
		updateGoogleSheetKPI(cfg, srv)
	} else {
		logit.Debug("Taking the datapoints branch!")
		updateGoogleSheetValues(cfg, srv)
	}

	logit.Info("Shutting down")
}

// cellValueToSheetLetter reads a sheet row and caches the Column
// letter for each cell value for conveniant lookup later on.
func cellValueToSheetLetter(cfg *Config, srv *sheets.Service,
	searchFor string, cache bool) string {

	rowToSearch := cfg.SheetName + "!" + "A" +
		cfg.SheetTopicRow + ":" + cfg.SheetTopicRow
	resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID, rowToSearch).Do()
	//resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID, rowToSearch).ValueRenderOption("UNFORMATTED_VALUE").Do()
	if err != nil {
		logit.WithFields(log.Fields{
			"cell":        rowToSearch,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Read col data from sheet")
	}

	offset := -1 // The offset for this topic
	if len(resp.Values) == 0 {
		logit.WithFields(log.Fields{
			"cell":        rowToSearch,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("Topic not found in sheet")
	} else {
		for colCounter, topic := range resp.Values[0] {

			if cache {
				if _, ok := topicCache[fmt.Sprintf("%v", topic)]; !ok {
					if topic != "" {
						topicCache[fmt.Sprintf("%v", topic)] = clmconv.Itoa(colCounter)
						logit.WithFields(log.Fields{
							"topic":  topic,
							"column": clmconv.Itoa(colCounter),
						}).Debug("Caching topic to column letter")
					}
				}
			}
			if searchFor == topic && offset == -1 {
				offset = colCounter
				if !cache {
					break // Break quickly if not a caching run
				}
			}
		}
	}
	if offset == -1 && !cache {
		logit.WithFields(log.Fields{
			"cell":        rowToSearch,
			"spreadsheet": cfg.SpreadsheetID,
		}).Fatal("FIX: Add a new column for topic")
	}

	//return clmconv.Itoa(dataStartColNum + offset)
	return clmconv.Itoa(offset)
}

// cellValueToSheetRow reads a sheet column and caches the Row
// number for each cell value for conveniant lookup later on.
func cellValueToSheetRow(SpreadsheetID string, SheetName string,
	SheetDataStartRow string, SheetKeyCol string, MatchAll string,
	srv *sheets.Service, searchFor string, cache bool) int {

	if cache {
		keyCacheMax = len(keyCache)
	}

	sheetDataStartRow, _ := strconv.Atoi(SheetDataStartRow)
	colToSearch := SheetName + "!" + SheetKeyCol +
		fmt.Sprintf("%d", sheetDataStartRow) + ":" + SheetKeyCol

	resp, err := srv.Spreadsheets.Values.Get(SpreadsheetID, colToSearch).Do()
	//resp, err := srv.Spreadsheets.Values.Get(SpreadsheetID, colToSearch).ValueRenderOption("UNFORMATTED_VALUE").Do()

	if err != nil {
		logit.WithFields(log.Fields{
			"cell":        colToSearch,
			"error":       err,
			"spreadsheet": SpreadsheetID,
		}).Fatal("Read row data from sheet")
	}

	logit.Debug("colToSearch: ", colToSearch)

	offset := -1 // The offset for this row
	if len(resp.Values) == 0 {
		logit.WithFields(log.Fields{
			"col":         colToSearch,
			"spreadsheet": SpreadsheetID,
		}).Fatal("No data found in col")
	} else {

		for rowCounter, row := range resp.Values {

			if cache && len(row) > 0 {
				if _, ok := keyCache[fmt.Sprintf("%v", row[0])]; !ok {

					keyCache[fmt.Sprintf("%v", row[0])] = sheetDataStartRow + rowCounter

					if keyCacheMax < rowCounter {
						keyCacheMax = rowCounter
					}
					logit.WithFields(log.Fields{
						"key": row[0],
						"row": sheetDataStartRow + rowCounter,
					}).Debug("Caching key to row number")
				}
				if MatchAll == "yes" {
					keyCacheArr[fmt.Sprintf("%v", row[0])] = append(keyCacheArr[fmt.Sprintf("%v", row[0])], sheetDataStartRow+rowCounter)
				}
			}

			if len(row) > 0 && searchFor == row[0] && offset == -1 {
				offset = rowCounter
				if !cache {
					break // Break quickly if not a caching run
				}
			}
		}
	}
	if offset == -1 && !cache {
		logit.WithFields(log.Fields{
			"cell":        colToSearch,
			"spreadsheet": SpreadsheetID,
		}).Fatal("FIX: Add a new row for key")
	}

	return sheetDataStartRow + offset
}

// updateGoogleSheetValues updates the Google Spreadsheet
func updateGoogleSheetValues(cfg *Config, srv *sheets.Service) {

	// Generic value holder
	var vr sheets.ValueRange
	vr.Values = make([][]interface{}, 1)

	sheetDataStartRow, _ := strconv.Atoi(cfg.SheetDataStartRow)

	// Prepare to cache values
	topicCache = make(map[string]string)
	keyCache = make(map[string]int)
	keyCacheArr = make(map[string][]int)

	// Update for all data types in the configuration
	for _, dp := range cfg.Datapoints {

		// Support for per data point override of key column.
		keyCol := cfg.SheetKeyCol
		if dp.KeyCol != "" {
			keyCol = dp.KeyCol
		}

		// Calculate the Column letter and Row number for a cell value
		_ = cellValueToSheetLetter(cfg, srv, dp.Title, true)
		_ = cellValueToSheetRow(cfg.SpreadsheetID, cfg.SheetName, cfg.SheetDataStartRow, keyCol, dp.MatchAll, srv, "", true)

		logit.WithFields(log.Fields{
			"title":       dp.Title,
			"col":         topicCache[dp.Title],
			"spreadsheet": cfg.SpreadsheetID,
			"command":     dp.Command + " " + dp.Args,
		}).Debug("Sheet and command info")

		var sheetValues [][]interface{}
		col := cfg.SheetName + "!" + topicCache[dp.Title] + cfg.SheetDataStartRow + ":" + topicCache[dp.Title]

		// Run the command
		if len(dp.Command) > 0 {

			// Run KPI colleting command
			cmd := exec.Command(dp.Command, dp.Args)
			tmpOut, err := cmd.CombinedOutput()
			if err != nil {
				logit.WithFields(log.Fields{
					"kpi":     dp.Title,
					"error":   err,
					"command": dp.Command + " " + dp.Args,
				}).Fatal("Running external command")
			}

			// resp contains an interface of all values in the column
			resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID, col).ValueRenderOption("UNFORMATTED_VALUE").Do()

			if err != nil {
				logit.WithFields(log.Fields{
					"col":         col,
					"error":       err,
					"hint":        "is sheet-topic-row set correctly?",
					"spreadsheet": cfg.SpreadsheetID,
				}).Fatal("Read column data from sheet")
			}
			sheetValues = resp.Values

			// If some of the last cells in the data row
			// has not values, the sheetValues array will be
			// too short and we need to extend it to hold
			// all the values we get.
			sheetValuesLen := len(sheetValues)
			if sheetValuesLen < keyCacheMax {
				logit.WithFields(log.Fields{
					"rows":        keyCacheMax + 1,
					"spreadsheet": cfg.SpreadsheetID,
					"col":         topicCache[dp.Title],
				}).Debug("Extending Sheetvalues")
				newSlice := make([][]interface{}, sheetValuesLen, keyCacheMax+1)
				copy(newSlice, sheetValues)
				sheetValues = newSlice
				sheetValues = sheetValues[0 : keyCacheMax+1]

				// Add storage type for all elements
				for i := sheetValuesLen; i <= keyCacheMax; i++ {
					sheetValues[i] = []interface{}{""}
				}
			}

			// Lets use github.com/tidwall/gjson to decode
			// JSON lines
			json := string(tmpOut)

			// Loop all results from the external command
			// and compare to the values from the sheet
			gjson.ForEachLine(json, func(line gjson.Result) bool {

				// Get the key value pair from the scraping command
				key := gjson.Get(line.String(), "key").String()
				val := gjson.Get(line.String(), "val").String()

				// Update sheet
				if sheetRowNum, ok := keyCache[key]; ok {

					scrapeNum := sheetRowNum - sheetDataStartRow

					// Um, uninitialized sheetValue, lets make room to hold a value.
					if len(sheetValues[scrapeNum]) == 0 {
						sheetValues[scrapeNum] = []interface{}{""}
					}

					if val != fmt.Sprintf("%v", sheetValues[scrapeNum][0]) {

						logit.WithFields(log.Fields{
							"row":         scrapeNum,
							"spreadsheet": cfg.SpreadsheetID,
							"col":         topicCache[dp.Title],
							"key":         key,
							"val":         val,
							"old-val":     sheetValues[scrapeNum][0],
						}).Debug("Updating value")

						sheetValues[scrapeNum][0] = val

					} else {
						logit.WithFields(log.Fields{
							"row":         scrapeNum,
							"spreadsheet": cfg.SpreadsheetID,
							"col":         topicCache[dp.Title],
							"key":         key,
							"val":         val,
							"old-val":     sheetValues[scrapeNum][0],
						}).Debug("NOT updating value")

					}

					if dp.MatchAll == "yes" {

						for _, rowNum := range keyCacheArr[key] {

							scrapeNum := rowNum - sheetDataStartRow

							for i := len(sheetValues); i <= scrapeNum; i++ {
								sheetValues = append(sheetValues, []interface{}{val})
								sheetValues = sheetValues[0:len(sheetValues)]
								keyCacheMax = len(sheetValues) - 1
							}

							if len(sheetValues[scrapeNum]) == 0 {
								sheetValues[scrapeNum] = []interface{}{""}
							}

							if val != fmt.Sprintf("%v", sheetValues[scrapeNum][0]) {

								logit.WithFields(log.Fields{
									"row":         scrapeNum,
									"spreadsheet": cfg.SpreadsheetID,
									"col":         topicCache[dp.Title],
									"key":         key,
									"val":         val,
									"old-val":     sheetValues[scrapeNum][0],
								}).Debug("Updating value")

								sheetValues[scrapeNum][0] = val

							} else {
								logit.WithFields(log.Fields{
									"row":         scrapeNum,
									"spreadsheet": cfg.SpreadsheetID,
									"col":         topicCache[dp.Title],
									"key":         key,
									"val":         val,
									"old-val":     sheetValues[scrapeNum][0],
								}).Debug("NOT updating value")

							}

						}
					}

				} else {

					if dp.AddRows == "yes" {
						logit.WithFields(log.Fields{
							"spreadsheet": cfg.SpreadsheetID,
							"col":         topicCache[dp.Title],
							"key":         key,
							"val":         val,
						}).Debug("Appending column")
						sheetValues = append(sheetValues, []interface{}{val})
						sheetValues = sheetValues[0:len(sheetValues)]
						keyCacheMax = len(sheetValues) - 1

						// If the column being updated is the keys column,
						// we have to add a new value to the keyCache
						if topicCache[dp.Title] == cfg.SheetKeyCol {
							keyCache[fmt.Sprintf("%v", val)] = sheetDataStartRow + keyCacheMax
							if dp.MatchAll == "yes" {
								keyCacheArr[fmt.Sprintf("%v", val)] = append(keyCacheArr[fmt.Sprintf("%v", val)], sheetDataStartRow+keyCacheMax)
							}
						}

						logit.WithFields(log.Fields{
							"row":         len(sheetValues),
							"spreadsheet": cfg.SpreadsheetID,
							"col":         topicCache[dp.Title],
							"key":         key,
							"val":         val,
						}).Debug("Adding new row")

					} else {

						logit.WithFields(log.Fields{
							"kpi": dp.Title,
							"key": key,
						}).Warning("Can not update column")
					}

				}

				return true
			})

		}

		// Updated values to set in the sheet
		vr.Values = sheetValues

		// We might hit the "Quota exceeded for quota group 'WriteGroup'"
		r, _ := regexp.Compile("Error 429|operation timed out")
		iterations := 12
		var err error
		for iterations > 0 {
			_, err = srv.Spreadsheets.Values.Update(cfg.SpreadsheetID,
				col, &vr).ValueInputOption("USER_ENTERED").Do()
			iterations--

			if err != nil && r.MatchString(err.Error()) {
				logit.Debug("Sleeping 10 sec")
				time.Sleep(10000 * time.Millisecond)
			} else {
				iterations = 0
			}
			if err != nil && iterations == 0 {

				// We might want to try again if we simply time out
				logit.WithFields(log.Fields{
					"col":         col,
					"error":       err,
					"spreadsheet": cfg.SpreadsheetID,
				}).Fatal("Update sheet column")
			}
		}

	}
}

// updateGoogleSheetKPI updates the Google Spreadsheet
func updateGoogleSheetKPI(cfg *Config, srv *sheets.Service) {

	// Construct the string matching this week ("YYYY-WW")
	tn := time.Now().UTC()
	year, week := tn.ISOWeek()
	nowYearWeek := fmt.Sprintf("%d-%02d", year, week)
	lastUpdateDate := time.Now().Format("2006-01-02")
	logit.WithFields(log.Fields{
		"date": nowYearWeek,
	}).Debug("Current week")

	// Calculate the Column letter for this week
	dataWeekColLetter := cellValueToSheetLetter(cfg, srv, nowYearWeek, false)

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
				cfg.SheetKeyCol+kpi.SheetRow+":"+
				cfg.SheetKeyCol+kpi.SheetRow,
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
				cfg.SheetLastUpdateCol+kpi.SheetRow+":"+
				cfg.SheetLastUpdateCol+kpi.SheetRow,
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
	logit.WithFields(log.Fields{
		"title":         kpi.Title,
		"JSON-endpoint": kpi.JSONEndpoint,
	}).Debug("Scraping endpoint")

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

	// Check if existing vakue is an empty value or if it is the
	// same value as we want to set.
	if overwrite == 0 {
		resp, err := srv.Spreadsheets.Values.Get(cfg.SpreadsheetID, cell).ValueRenderOption("UNFORMATTED_VALUE").Do()

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

			return errorCode["collision"]
		}
	}
	logit.WithFields(log.Fields{
		"cell": cell, "kpi": kpi.Title,
	}).Info(action)

	vr.Values[0] = value
	_, err := srv.Spreadsheets.Values.Update(cfg.SpreadsheetID, cell, vr).ValueInputOption("USER_ENTERED").Do()
	if err != nil {
		logit.WithFields(log.Fields{
			"kpi":         kpi.Title,
			"error":       err,
			"spreadsheet": cfg.SpreadsheetID,
			"sheet":       cell,
			"value":       value,
		}).Fatal("Writing sheet cell")
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
	logit.WithFields(log.Fields{
		"body": response.Body,
	}).Debug("Response body")

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
