# Usage

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

	goStatsReportInterval, _ := time.ParseDuration("3s")

	config := &errplane.InfluxDBConfig{
		Host:     "localhost:8086",
		Database: "metrics",
		Username: "root",
		Password: "root",
	}
	ep := errplane.New(config)

	ep.ReportRuntimeStats("runtime", goStatsReportInterval)

	doSomeJob(20)
}

```

[Grafana](https://github.com/torkelo/grafana) config example:

https://gist.github.com/lidashuang/9826178
