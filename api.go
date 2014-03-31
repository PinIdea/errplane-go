package errplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"time"
)

type PointValues []interface{}

type JsonPoints struct {
	Name    string        `json:"name"`
	Columns []string      `json:"columns"`
	Points  []PointValues `json:"points"`
}

var METRIC_REGEX, _ = regexp.Compile("^[a-zA-Z0-9._]*$")

type Errplane struct {
	proto               string
	url                 string
	Timeout             time.Duration
	closeChan           chan bool
	msgChan             chan *JsonPoints
	closed              bool
	timeout             time.Duration
	runtimeStatsRunning bool
	dbConf              *InfluxDBConfig
}

const (
	DEFAULT_HTTP_HOST = "localhost:8086"
)

type InfluxDBConfig struct {
	Host     string
	Database string
	Username string
	Password string
}

func New(config *InfluxDBConfig) *Errplane {
	return newCommon("http", config)
}

func newCommon(proto string, dbConfig *InfluxDBConfig) *Errplane {
	ep := &Errplane{
		proto:     proto,
		Timeout:   1 * time.Second,
		msgChan:   make(chan *JsonPoints),
		closeChan: make(chan bool),
		closed:    false,
		timeout:   2 * time.Second,
		dbConf:    dbConfig,
	}
	ep.SetHttpHost(dbConfig.Host)
	// ep.setTransporter(nil)
	go ep.processMessages()
	return ep
}

// call from a goroutine, this method never returns
func (self *Errplane) processMessages() {
	posts := make([]*JsonPoints, 0)
	for {

		select {
		case x := <-self.msgChan:
			posts = append(posts, x)
			if len(posts) < 100 {
				continue
			}
			self.flushPosts(posts)
		case <-time.After(1 * time.Second):
			self.flushPosts(posts)
		case <-self.closeChan:
			self.flushPosts(posts)
			self.closeChan <- true
			return
		}

		posts = make([]*JsonPoints, 0)
	}
}

func (self *Errplane) flushPosts(posts []*JsonPoints) {
	if len(posts) == 0 {
		return
	}

	// do the http ones first
	httpPoint := self.mergeMetrics(posts)

	if httpPoint != nil {
		if err := self.SendHttp(httpPoint); err != nil {
			fmt.Fprintf(os.Stderr, "Error while posting points to Errplane. Error: %s\n", err)
		}
	}
}

func (self *Errplane) mergeMetrics(points []*JsonPoints) []*JsonPoints {
	if len(points) == 0 {
		return nil
	}

	metricToPoints := make(map[string][]PointValues)

	for _, jsonPoints := range points {
		name := jsonPoints.Name
		metricToPoints[name] = append(metricToPoints[name], jsonPoints.Points...)
	}

	mergedMetrics := make([]*JsonPoints, 0)

	for metric, pValues := range metricToPoints {
		mergedMetrics = append(mergedMetrics, &JsonPoints{
			Name:    metric,
			Columns: []string{"value", "time"},
			Points:  pValues,
		})
	}

	return mergedMetrics
}

func (self *Errplane) SendHttp(data []*JsonPoints) error {
	buf, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("Cannot marshal %#v. Error: %s", data, err)
	}

	resp, err := http.Post(self.url, "application/json", bytes.NewReader(buf))
	if err != nil {
		resp.Body.Close()
		return err
	}
	return responseToError(resp)
}

func responseToError(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}

	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("Server returned (%d): %s", response.StatusCode, string(body))
}

// Close the errplane object and flush all buffered data points
func (self *Errplane) Close() {
	self.closed = true
	// tell the go routine to finish
	self.closeChan <- true
	// wait for the go routine to finish
	<-self.closeChan
}

func (self *Errplane) SetHttpHost(host string) {
	params := url.Values{}
	params.Set("u", self.dbConf.Username)
	params.Set("p", self.dbConf.Password)
	self.url = fmt.Sprintf("%s://%s/db/%s/series?%s", self.proto, host, self.dbConf.Database, params.Encode())
}

// Start a goroutine that will post runtime stats to errplane, stats include memory usage, garbage collection, number of goroutines, etc.
// Args:
//   prefix: the prefix to use in the metric name
//   sleep: the sampling frequency
func (self *Errplane) ReportRuntimeStats(prefix string, sleep time.Duration) {
	if self.runtimeStatsRunning {
		fmt.Fprintf(os.Stderr, "Runtime stats is already running\n")
		return
	}

	self.runtimeStatsRunning = true
	go self.reportRuntimeStats(prefix, sleep)
}

func (self *Errplane) StopRuntimeStatsReporting() {
	self.runtimeStatsRunning = false
}

func (self *Errplane) reportRuntimeStats(prefix string, sleep time.Duration) {
	memStats := &runtime.MemStats{}
	lastSampleTime := time.Now()
	var lastPauseNs uint64 = 0
	var lastNumGc uint32 = 0

	nsInMs := float64(time.Millisecond)

	for self.runtimeStatsRunning {
		runtime.ReadMemStats(memStats)

		now := time.Now()

		self.Report(fmt.Sprintf("%s.goroutines", prefix), float64(runtime.NumGoroutine()), now)
		self.Report(fmt.Sprintf("%s.memory.heap.objects", prefix), float64(memStats.HeapObjects), now)
		self.Report(fmt.Sprintf("%s.memory.allocated", prefix), float64(memStats.Alloc), now)
		self.Report(fmt.Sprintf("%s.memory.mallocs", prefix), float64(memStats.Mallocs), now)
		self.Report(fmt.Sprintf("%s.memory.frees", prefix), float64(memStats.Frees), now)
		self.Report(fmt.Sprintf("%s.memory.gc.total_pause", prefix), float64(memStats.PauseTotalNs)/nsInMs, now)
		self.Report(fmt.Sprintf("%s.memory.heap", prefix), float64(memStats.HeapAlloc), now)
		self.Report(fmt.Sprintf("%s.memory.stack", prefix), float64(memStats.StackInuse), now)

		if lastPauseNs > 0 {
			pauseSinceLastSample := memStats.PauseTotalNs - lastPauseNs
			self.Report(fmt.Sprintf("%s.memory.gc.pause_per_second", prefix), float64(pauseSinceLastSample)/nsInMs/sleep.Seconds(), now)
		}
		lastPauseNs = memStats.PauseTotalNs

		countGc := int(memStats.NumGC - lastNumGc)
		if lastNumGc > 0 {
			diff := float64(countGc)
			diffTime := now.Sub(lastSampleTime).Seconds()
			self.Report(fmt.Sprintf("%s.memory.gc.gc_per_second", prefix), diff/diffTime, now)
		}

		// get the individual pause times
		if countGc > 0 {
			if countGc > 256 {
				fmt.Fprintf(os.Stderr, "We're missing some gc pause times")
				countGc = 256
			}

			for i := 0; i < countGc; i++ {
				idx := int((memStats.NumGC-uint32(i))+255) % 256
				pause := float64(memStats.PauseNs[idx])
				self.Report(fmt.Sprintf("%s.memory.gc.pause", prefix), pause/nsInMs, now)
			}
		}

		// keep track of the previous state
		lastNumGc = memStats.NumGC
		lastSampleTime = now

		time.Sleep(sleep)
	}
}

func (self *Errplane) Report(metric string, value float64, timestamp time.Time) error {
	return self.sendCommon(metric, value, &timestamp)
}

func (self *Errplane) sendCommon(metric string, value float64, timestamp *time.Time) error {
	if err := verifyMetricName(metric); err != nil {
		return err
	}

	var now int64
	if timestamp != nil {
		now = getCurrentTime()
	}

	now = getCurrentTime()

	data := &JsonPoints{
		Name:   metric,
		Points: []PointValues{{value, now}},
	}
	self.msgChan <- data
	return nil
}

func verifyMetricName(name string) error {
	if len(name) > 255 {
		return fmt.Errorf("Metric names must be less than 255 characters")
	}

	if !METRIC_REGEX.MatchString(name) {
		return fmt.Errorf("Invalid metric name %s. See docs for valid metric names", name)
	}

	return nil
}

func getCurrentTime() int64 {
	return time.Now().UnixNano() / 1000000
}
