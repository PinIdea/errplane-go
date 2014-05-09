# Usage


监控Golang运行信息，存储到[influxdb](http://influxdb.org/)，现在使用的是Http协议。

influxdb数据可以使用 [Grafana](https://github.com/torkelo/grafana) 展示

config example:

https://gist.github.com/lidashuang/9826178


example:

```go
package main

import (
	"log"
	"math/rand"
	"runtime"
	"time"

	errplane "github.com/PinIdea/go-metrics-influxdb"
)

func allocateAndSum(arraySize int) int {
	arr := make([]int, arraySize, arraySize)
	for i, _ := range arr {
		arr[i] = rand.Int()
	}
	time.Sleep(time.Duration(rand.Intn(3000)) * time.Millisecond)

	result := 0
	for _, v := range arr {
		result += v
	}
	return result
}

var m = &runtime.MemStats{}

func doSomeJob(numRoutines int) {
	for {
		runtime.ReadMemStats(m)
		log.Println("num goroutine:", runtime.NumGoroutine())
		for i := 0; i < numRoutines; i++ {
			go allocateAndSum(rand.Intn(1024) * 1024)
		}
		time.Sleep(1000 * time.Millisecond)
		runtime.GC()
	}
}

func main() {
	
	// package usage 
	goStatsReportInterval, _ := time.ParseDuration("3s")

	config := &errplane.InfluxDBConfig{
		Host:     "localhost:8086",
		Database: "metrics",
		Username: "root",
		Password: "root",
	}
	ep := errplane.New(config)

	ep.ReportRuntimeStats("runtime", goStatsReportInterval)

	// demo 
	doSomeJob(20)
}

```
