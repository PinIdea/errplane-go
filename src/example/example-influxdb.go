package main

import (
	// "log"
	"math/rand"
	"os"
	"runtime"
	"time"

	errplane "../.."
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
	//log.Printf("Array size is: %d, sum is: %d\n", arraySize, result)
	return result
}

var m = &runtime.MemStats{}

func doSomeJob(numRoutines int) {
	for {
		runtime.ReadMemStats(m)
		// log.Printf("Alloc: %d MB", m.Alloc/1024/1024)
		// log.Println("num goroutine:", runtime.NumGoroutine())
		for i := 0; i < numRoutines; i++ {
			go allocateAndSum(rand.Intn(1024) * 1024)
		}
		// log.Printf("All %d routines started\n", numRoutines)
		time.Sleep(1000 * time.Millisecond)
		runtime.GC()
	}
}

func main() {

	host, _ := os.Hostname()
	goStatsReportInterval, _ := time.ParseDuration("1s")

	config := &errplane.InfluxDBConfig{
		Host:     "localhost:8086",
		Database: "metrics",
		Username: "root",
		Password: "root",
	}
	ep := errplane.New(config)

	ep.ReportRuntimeStats("runtime", "",
		errplane.Dimensions{"host": host}, goStatsReportInterval)

	doSomeJob(20)
}
