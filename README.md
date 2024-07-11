# x2i

## What is x2i?
x2i sends raw data from Gatling, JMeter and K6 load generation tools to
[InfluxDB time series database](https://www.influxdata.com/). x2i is the default solution for Perfana customers to
send load generation tool results to Perfana and use this data to automatically analyse performance tests. x2i can
also be used independently of Perfana.

## History
x2i evolved from Anton Kramarev's [gatling-to-influxdb](https://github.com/Dakaraj/gatling-to-influxdb), a project
that processes the Gatling log file and sends the raw data to influxdb for analysis. Anton's goal was to replace the
Graphite backend plugin in Gatling that allows aggregated data to send to influxdb. Using raw data allows for more
detailed analysis of test results using Influxdb.

At Perfana we forked the project, added support for additional load generation tools, and made several other
improvements. Hence the name 'x2i', where the 'x' stands for multiple load generation tools.

## Need support?
Existing Perfana customers and trial users best contact Perfana directly in case of any questions or issues. If you
are not (yet ;-) a Perfana customer or trial user, feel free to create an issue in this Github repository. Please limit
an issue to x2i specifically, we won't be able to troubleshoot your custom integration.

## Supported platforms
Binaries for multiple operating systems and architectures are provided. In production x2i is mostly used on
AMD64 Linux. Our development systems also include MacOS on ARM. Other platforms are used sporadically and could have
unknown issues.

## Influxdb requirements
x2i requires an existing database with read/write access. The read access is used to verify the connection to the
database.

The following versions of InfluxDB are known to work:
* InfluxDB OSS 1.8.10

Later versions of InfluxDB are not supported.

## Usage
Run `x2i -h` for a quick help with examples for the different load generation tools that are supported.

### Generic
x2i needs only one argument, the location where to find the results of the testrun. In a common scenario you will
provide additional arguments for:
* the address, database, username and password for influxdb
* the test tool that is used; this can be omitted if gatling is used

The `--system-under-test` and `--test-environment` arguments could be used, and are required for the Perfana integration
to work properly.

Datapoints are send to Influxdb when either the hardcoded timeout of 5 seconds is reached, or when `--max-batch-size`
number of datapoints is logged. The number of datapoints that is being send is logged in the x2i log file.

### Gatling
* Requires version 3.5.0 or above.
* In gatling.conf enable the DataWriters to file; if you change the directory => results value, make sure you set the
correct path as argument to x2i

### JMeter
Add a <i>View Results Tree</i> component that logs errors and successes to the Filename
`~/results/${__time(yyyy-MM-dd-HH-mm)}/resultsTree.csv`. Configure it by enabling the following fields:
* Save Elapsed Time
* Save Response Messages
* Save Success
* Save sent by count
* Save Idle Time
* Save Assertion Results (XML)
* Save Field Names (CSV)
* Save Label
* Save Thread Name
* Save Assertion Failure Message
* Save Active Thread Counts
* Save Latency
* Save Time Stamp
* Save Response Code
* Save Data Type
* Save received byte count
* Save URL
* Save Connect Time
* Save Sub Results

### K6
Use `k6 run --out csv=test_results.csv`.

## Integration

### Perfana
When using x2i with Perfana, x2i will be started using a CommandRunnerEventConfig in your POM. In the starter
packages for gatling, jmeter and k6, you will find a perfect example. The `--system-under-test` and `--test-environment`
arguments are required for the Perfana integration to work properly.

### CI/CD
You could integrate x2i in your CI/CD pipeline. When running in detached mode, x2i will return the PID as follows:

```bash
[PID]	20201
```

allowing you to stop the process when the test has finished.

```bash
echo "Starting x2i in detached mode, saving PID in variable" && \
x2iPID=$(x2i <arguments> -d | awk '{print $2}') && \
echo "Build and execute test" && \
<run your test> \
echo "Waiting for parser to safely finish all its work" && \
sleep 30 && \
echo "Sending interrupt signal to x2i process" && \
kill -INT $x2iPID && \
echo "Waiting for process to stop safely" && \
sleep 30 && \
echo "Exiting"
```

## Building application
Building the application should be straightforward. Install the go language development tools and run `go build`.

## Want to contribute?
We welcome contributions from users of x2i. If you would like to contribute to this project, we encourage you to 
create an issue to discuss the contribution first.

## How to release new version?
* Commit your changes to main.
* Make sure you update the version number in cmd/root.go, around line 96.
* Create new tag with the new version (e.g. x2i-1.2.3), this triggers a new build and publishes the binaries.

## License
This application is licensed under MIT license. Please read the LICENSE file for details.