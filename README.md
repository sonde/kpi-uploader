# KPI uploader
`kpi-uploader` uploads KPI values to a Google spreadsheet.

`kpi-uploader` reads `config.yaml` and uploads this weeks values for
one or more KPIs to the correct week in the specified Google
spreadsheet and sheet name. The Google spreadsheet needs to be shared
with Edit rights with a G Suite service account using a secret.json
file (see below for how to create a Google service account).

The `config.yaml` config file:
```
# The ID of the Google spreadsheet containing the KPI data logging setup
spreadsheet-id: "1V5Uu8Wu20S95vJ45gGm_OiRr2QUsLd5PxUhhK3EHBxc"
# The name of the sheet that will contain the weekly KPI data
sheet-name: "KPI data"
sheet-kpi-last-update-col: "B" # The column where 'Last update' will be logged
sheet-kpi-name-col: "C"        # The column where 'KPI title' will be logged
sheet-data-start-col: "E"      # The column for the first week of data
sheet-data-date-row: 2         # The row where year-week numbers are

KPI:
  - KPI1:
    title: "Title for KPI 1" # KPI name
    sheet-row: 3                     # The sheet row reserved for this KPI
    kpi-command: "cat"               # The command to run
    kpi-command-args: "var/file.txt" # Arguments to the command
  - KPI2:
    title: "Number of legacy servers"
    json-endpoint: "https://prometheus.company.com/query?query=count(up{job=%27prometheus_node_exporter%27})"
    json-data-picker: "data.result.0.value.1"
    # Format: {"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1581097668.761,"321"]}]}}
[...]
```

Make sure the `spreadsheet-id` corresponds to a API uploader compatible Google
spreadsheet, see the provided [example Google KPI spreadsheet](https://docs.google.com/spreadsheets/d/1V5Uu8Wu20S95vJ45gGm_OiRr2QUsLd5PxUhhK3EHBxc/edit#gid=750502480) for Google spreadsheet structure.
Make sure `sheet-name` corresponds to the sheet name for KPI data in the spreadsheet.
See `config.yaml-example` for ideas on how to use.

## Create a G Suite service account
You need to create a G Suite service account, for instance follow
[Create a new project in Google Developer Console](https://www.prudentdevs.club/gsheets-go).
1. Make sure to name the secrets JSON file `secret.json`
2. Make sure to Share your Google spreadsheet with the `client_email` specified in the `secrets.json` file (*something*@*something*.iam.gserviceaccount.com).

## Build and run
```
$ ls
LICENSE	  README.md   config.yaml   main.go   secret.json   var/
$ go mod init
$ go mod tidy
$ go build ./... && ./kpi-uploader
```

## Examples
You can change the logging method and log level by setting LOG\_FORMAT and LOG\_LEVEL environment variables, the default log level is FATAL.
```
$ go build ./... && LOG_LEVEL=INFO ./kpi-uploader
INFO[0000] Starting up               @version=1 logger=kpi-uploader
INFO[0000] Current Week is 2020-07   @version=1 logger=kpi-uploader
INFO[0000] Setting KPI title         @version=1 cell="Cloud migration data!C7:C7" kpi="Number of servers in D7" kpiNum=4 logger=kpi-uploader
INFO[0000] Setting KPI value         @version=1 cell="Cloud migration data!K7:K7" kpiNum=4 logger=kpi-uploader value=321 week=2020-07
INFO[0001] Setting last updated date @version=1 cell="Cloud migration data!B7:B7" kpiNum=4 logger=kpi-uploader value=2020-02-13
WARN[0000] No command to run         @version=1 kpi="Number of applications in legacy" logger=kpi-uploader
[...]

$ LOG_FORMAT=json LOG_LEVEL=INFO ./kpi-uploader
{"@timestamp":"2020-02-13T17:43:18.009689+01:00","@version":"1","caller":"main.main","file":".../kpi-uploader/main.go:146","level":"info","logger":"kpi-uploader","message":"Starting up"}
{"@timestamp":"2020-02-13T17:43:18.01076+01:00","@version":"1","caller":"main.updateKPIGoogleSheet","file":".../kpi-uploader/main.go:165","level":"info","logger":"kpi-uploader","message":"Current Week is 2020-07"}
{"@timestamp":"2020-02-13T17:43:18.646178+01:00","@version":"1","caller":"main.updateKPIGoogleSheet","cell":"Cloud migration data!C7:C7","file":".../kpi-uploader/main.go:251","kpi":"Number of servers in D7","kpiNum":4,"level":"info","logger":"kpi-uploader","message":"Setting KPI title"}
{"@timestamp":"2020-02-13T17:43:18.953471+01:00","@version":"1","caller":"main.updateKPIGoogleSheet","cell":"Cloud migration data!K7:K7","file":".../kpi-uploader/main.go:273","kpiNum":4,"level":"info","logger":"kpi-uploader","message":"Setting KPI value","value":321,"week":"2020-07"}
{"@timestamp":"2020-02-13T17:43:19.202086+01:00","@version":"1","caller":"main.updateKPIGoogleSheet","cell":"Cloud migration data!B7:B7","file":".../kpi-uploader/main.go:296","kpiNum":4,"level":"info","logger":"kpi-uploader","message":"Setting last updated date","value":"2020-02-13"}
{"@timestamp":"2020-02-13T17:43:19.451793+01:00","@version":"1","caller":"main.updateKPIGoogleSheet","file":".../kpi-uploader/main.go:240","kpi":"Number of applications in D7","level":"warning","logger":"kpi-uploader","message":"No command to run"}
[...]
```

The resulting Google spreadsheet data sheet will look similar to this after a week `2020-06` run:

Last update | KPI                                              | Month Week | Jan `2020-04` | Jan `2020-05` | Feb `2020-06` | Feb `2020-07`
:---------: | :----------------------------------------------- | ---:  | ------: | ------: |    ---: | ---:
2020-02-05  | Number of applications not migrated              |       | 29      | 29      | 26      |
2020-02-05  | Number of servers in old datacenter              |       | 350     | 340     | 330     |
2020-02-05  | Number of applications migrated to cloud         |       | 0       | 0       |       0 |
2020-02-05  | Number of apps passing cloud acceptance criteria |       | 100     | 110     | 123 |
2020-02-05  | Number of applications and servers in old DC     |       | 800     | 700     | 600 |

Next week, when run, `kpi-uploader` will add numbers to the `2020-07`
column for the specified KPIs.

## Improvements
This is a crude implementation, possible enhancements could include:
1. Built in support for querying and parsing of Prometheus metrics etc
2. Support for only uplading oen KPI at a time
3. Abort the update if the KPI title has conflicting content
