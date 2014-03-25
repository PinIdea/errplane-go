package errplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"time"
)

type WriteOperation struct {
	Writes []*JsonPoints `json:"writes"`
}

type JsonPoint struct {
	Value      float64           `json:"value"`
	Context    string            `json:"context"`
	Time       int64             `json:"time"`
	Dimensions map[string]string `json:"dimensions"`
}

type JsonPoints struct {
	Name    string       `json:"name"`
	Columns []string     `json:"columns"`
	Points  []*JsonPoint `json:"points"`
}

type Dimensions map[string]string

var METRIC_REGEX, _ = regexp.Compile("^[a-zA-Z0-9._]*$")

type Errplane struct {
	proto               string
	url                 string
	Timeout             time.Duration
	closeChan           chan bool
	msgChan             chan *WriteOperation
	closed              bool
	timeout             time.Duration
	runtimeStatsRunning bool
	DBConfig            *InfluxDBConfig
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
		msgChan:   make(chan *WriteOperation),
		closeChan: make(chan bool),
		closed:    false,
		timeout:   2 * time.Second,
		DBConfig:  dbConfig,
	}
	ep.SetHttpHost(dbConfig.Host)
	// ep.setTransporter(nil)
	go ep.processMessages()
	return ep
}

// call from a goroutine, this method never returns
func (self *Errplane) processMessages() {
	posts := make([]*WriteOperation, 0)
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

		posts = make([]*WriteOperation, 0)
	}
}

func (self *Errplane) flushPosts(posts []*WriteOperation) {
	if len(posts) == 0 {
		return
	}

	buf, _ := json.MarshalIndent(posts, "", "  ")
	fmt.Println("json:", string(buf))

	// do the http ones first
	httpPoint := self.mergeMetrics(posts)

	if httpPoint != nil {
		if err := self.SendHttp(httpPoint); err != nil {
			fmt.Fprintf(os.Stderr, "Error while posting points to Errplane. Error: %s\n", err)
		}
	}
}

func (self *Errplane) mergeMetrics(operations []*WriteOperation) *WriteOperation {
	if len(operations) == 0 {
		return nil
	}

	metricToPoints := make(map[string][]*JsonPoint)

	for _, operation := range operations {
		for _, jsonPoints := range operation.Writes {
			name := jsonPoints.Name
			metricToPoints[name] = append(metricToPoints[name], jsonPoints.Points...)
		}
	}

	mergedMetrics := make([]*JsonPoints, 0)

	for metric, points := range metricToPoints {
		mergedMetrics = append(mergedMetrics, &JsonPoints{
			Name:    metric,
			Columns: []string{"value", "time", "dimensions"},
			Points:  points,
		})
	}

	return &WriteOperation{
		Writes: mergedMetrics,
	}
}

func (self *Errplane) SendHttp(data *WriteOperation) error {
	buf, err := json.MarshalIndent(data.Writes, "", "  ")
	fmt.Println("after merge:\n", string(buf))
	if err != nil {
		return fmt.Errorf("Cannot marshal %#v. Error: %s", data, err)
	}

	// todo
	// fmt.Println("json:", string(buf))
	resp, err := http.Post(self.url, "application/json", bytes.NewReader(buf))
	return responseToError(resp, err, true)
}

func responseToError(response *http.Response, err error, closeResponse bool) error {
	if err != nil {
		return err
	}
	if closeResponse {
		defer response.Body.Close()
	}
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
	params.Set("u", self.DBConfig.Username)
	params.Set("p", self.DBConfig.Password)
	self.url = fmt.Sprintf("%s://%s/db/%s/series?%s", self.proto, host, self.DBConfig.Database, params.Encode())
}

func (self *Errplane) SetProxy(proxy string) error {
	proxyUrl, err := url.Parse(proxy)
	if err != nil {
		return err
	}
	self.setTransporter(proxyUrl)
	return nil
}

func (self *Errplane) SetTimeout(timeout time.Duration) error {
	self.timeout = timeout
	self.setTransporter(nil)
	return nil
}

func (self *Errplane) setTransporter(proxyUrl *url.URL) {
	transporter := &http.Transport{}
	if proxyUrl != nil {
		transporter.Proxy = http.ProxyURL(proxyUrl)
	}
	transporter.Dial = func(network, addr string) (net.Conn, error) {
		conn, err := net.DialTimeout(network, addr, self.timeout)
		if err != nil {
			return nil, err
		}
		conn.SetDeadline(time.Now().Add(self.timeout))
		return conn, nil
	}
	http.DefaultTransport = transporter
}

// Start a goroutine that will post runtime stats to errplane, stats include memory usage, garbage collection, number of goroutines, etc.
// Args:
//   prefix: the prefix to use in the metric name
//   context: all points will be reported with the given context name
//   dimensions: all points will be reported with the given dimensions
//   sleep: the sampling frequency
func (self *Errplane) ReportRuntimeStats(prefix, context string, dimensions Dimensions, sleep time.Duration) {
	if self.runtimeStatsRunning {
		fmt.Fprintf(os.Stderr, "Runtime stats is already running\n")
		return
	}

	self.runtimeStatsRunning = true
	go self.reportRuntimeStats(prefix, context, dimensions, sleep)
}

func (self *Errplane) StopRuntimeStatsReporting(prefix, context string, dimensions Dimensions, sleep time.Duration) {
	self.runtimeStatsRunning = false
}

func (self *Errplane) reportRuntimeStats(prefix, context string, dimensions Dimensions, sleep time.Duration) {
	memStats := &runtime.MemStats{}
	lastSampleTime := time.Now()
	var lastPauseNs uint64 = 0
	var lastNumGc uint32 = 0

	nsInMs := float64(time.Millisecond)

	for self.runtimeStatsRunning {
		runtime.ReadMemStats(memStats)

		now := time.Now()

		self.Report(fmt.Sprintf("%s.goroutines", prefix), float64(runtime.NumGoroutine()), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.heap.objects", prefix), float64(memStats.HeapObjects), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.allocated", prefix), float64(memStats.Alloc), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.mallocs", prefix), float64(memStats.Mallocs), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.frees", prefix), float64(memStats.Frees), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.gc.total_pause", prefix), float64(memStats.PauseTotalNs)/nsInMs, now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.heap", prefix), float64(memStats.HeapAlloc), now, context, dimensions)
		self.Report(fmt.Sprintf("%s.memory.stack", prefix), float64(memStats.StackInuse), now, context, dimensions)

		if lastPauseNs > 0 {
			pauseSinceLastSample := memStats.PauseTotalNs - lastPauseNs
			self.Report(fmt.Sprintf("%s.memory.gc.pause_per_second", prefix), float64(pauseSinceLastSample)/nsInMs/sleep.Seconds(), now, context, dimensions)
		}
		lastPauseNs = memStats.PauseTotalNs

		countGc := int(memStats.NumGC - lastNumGc)
		if lastNumGc > 0 {
			diff := float64(countGc)
			diffTime := now.Sub(lastSampleTime).Seconds()
			self.Report(fmt.Sprintf("%s.memory.gc.gc_per_second", prefix), diff/diffTime, now, context, dimensions)
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
				self.Report(fmt.Sprintf("%s.memory.gc.pause", prefix), pause/nsInMs, now, context, dimensions)
			}
		}

		// keep track of the previous state
		lastNumGc = memStats.NumGC
		lastSampleTime = now

		time.Sleep(sleep)
	}
}

// FIXME: make timestamp, context and dimensions optional (accept empty values, e.g. nil)
func (self *Errplane) Report(metric string, value float64, timestamp time.Time, context string, dimensions Dimensions) error {
	return self.sendCommon(metric, value, &timestamp, context, dimensions)
}

func (self *Errplane) sendCommon(metric string, value float64, timestamp *time.Time, context string, dimensions Dimensions) error {
	fmt.Println(metric, value)
	if err := verifyMetricName(metric); err != nil {
		return err
	}
	point := &JsonPoint{
		Value:      value,
		Context:    context,
		Dimensions: dimensions,
	}

	if timestamp != nil {
		point.Time = timestamp.Unix()
	}

	data := &WriteOperation{
		Writes: []*JsonPoints{
			&JsonPoints{
				Name:    metric,
				Columns: []string{"value", "time", "dimensions"},
				Points:  []*JsonPoint{point},
			},
		},
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
