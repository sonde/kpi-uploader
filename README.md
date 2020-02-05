# KPI uploader
`kpi-uploader` uploads KPI values to a Google spreadsheet.

`kpi-uploader` reads `config.yaml` and uploads this weeks values for
one or more KPIs to the correct week in the specified Google
spreadsheet and sheet name.

The config file:
```
# The ID of the Google spreadsheet containing the KPI data logging setup
spreadsheet-id: "1CYQIbU3KIxxpab2JC0jSUNSIbW0x4SbsRa6AYedA_a3"
# The name of the sheet that will contain the weekly KPI data
sheet-name: "Cloud migration KPIs"
sheet-kpi-last-update-col: "B" # The column where 'Last update' will be logged
sheet-kpi-name-col: "C"        # The column where 'KPI title' will be logged
sheet-data-start-col: "E"      # The column for the first week of data
sheet-data-date-row: 2         # The row where year-week numbers are

KPI:
  - KPI1:
    title: "The KPI title for KPI 1" # KPI name
    sheet-row: 3                     # The sheet row reserved for this KPI
    kpi-command: "cat"               # The command to run
    kpi-command-args: "var/file.txt" # Arguments to the command
  - KPI2:
[...]
```

Make sure the `spreadsheet-id` corresponds to a API uploader compatible Google 
spreadsheet, you can find n example here (TBD).
Make sure `sheet-name` corresponds to the sheet name you use to store KPI data.
See `config.yaml-example` for ideas on how to use.

## Build and run
```
go mod init
go mod tidy
go build ./... && ./kpi-uploader
```

## Example
```
$ go build ./... && ./kpi-uploader
KPI 1: Setting cell 'Cloud migration KPIs!C3:C3' to: 'Number of applications not migrated' (KPI title)
KPI 1: Setting cell 'Cloud migration KPIs!J3:J3' to: 26 (KPI value for week 2020-06)
KPI 1: Setting cell 'Cloud migration KPIs!B3:B3' to: 05-02-2020 (last update)
KPI 2: Setting cell 'Cloud migration KPIs!C4:C4' to: 'Number of servers in old datacenter' (KPI title)
KPI 2: Setting cell 'Cloud migration KPIs!J4:J4' to: 330 (KPI value for week 2020-06)
KPI 2: Setting cell 'Cloud migration KPIs!B4:B4' to: 05-02-2020 (last update)
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
