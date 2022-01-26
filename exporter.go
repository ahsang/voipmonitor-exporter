// A minimal example of how to include Prometheus instrumentation.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type VoipMonitorSession struct {
	SID string
}
type SIPResponse struct {
	Count              float64 `json:"cnt_all"`
	LastSIPresponse    string  `json:"lastSIPresponse"`
	LastSIPresponseNum int     `json:"lastSIPresponseNum"`
}

type CallStats struct {
	Total   int           `json:"total"`
	Results []SIPResponse `json:"results"`
}

const namespace = "voipmonitor"

var (
	listenAddress = flag.String("web.listen-address", ":9141",
		"Address to listen on for telemetry")
	metricsPath = flag.String("web.telemetry-path", "/metrics",
		"Path under which to expose metrics")

	// Metrics
	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last Voipmonitor query successful.",
		nil, nil,
	)
	callStatsReceived = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "call_stats_total"),
		"How many calls have occured (per last sip response code).",
		[]string{"last_sip_response", "sip_response_code"}, nil,
	)
)

type Exporter struct {
	vmEndpoint, vmUsername, vmPassword string
}

func NewExporter(vmEndpoint string, vmUsername string, vmPassword string) *Exporter {
	return &Exporter{
		vmEndpoint: vmEndpoint,
		vmUsername: vmUsername,
		vmPassword: vmPassword,
	}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- callStatsReceived
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	err := e.HitVoipmonitorRestApisAndUpdateMetrics(ch)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 0,
		)
		log.Println(err)
		return
	} else {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 1,
		)
	}

}

func (e *Exporter) HitVoipmonitorRestApisAndUpdateMetrics(ch chan<- prometheus.Metric) error {
	// Load channel stats
	url := e.vmEndpoint + "/php/model/sql.php?module=bypass_login&user=" + e.vmUsername + "&pass=" + e.vmPassword
	method := "POST"
	headers := make(map[string]string)
	var vms VoipMonitorSession
	payload := &bytes.Buffer{}
	res, err := makeHttpRequest(url, method, payload, headers)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return err
	}
	if err := json.Unmarshal(body, &vms); err != nil { // Parse []byte to go struct pointer
		fmt.Println("Can not unmarshal JSON")
		return err
	}

	now := time.Now().UTC()
	count, err := strconv.Atoi(os.Getenv("VOIPMONITOR_INTERVAL"))
	if err != nil {
		count = 5 // default interval
	}
	then := now.Add(time.Duration(-count) * time.Minute)

	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("task", "LISTING")
	_ = writer.WriteField("module", "CDR_stats")
	_ = writer.WriteField("fdatefrom", then.Format(time.RFC3339))
	// _ = writer.WriteField("fsensor_id", "18")
	_ = writer.WriteField("group_by", "4")
	_ = writer.WriteField("needColumns", "%5B%22lastSIPresponse%22%2C%22cnt_all%22%2C%22cnt_ok%22%lastSIPresponseNum%22%sensor_id")
	_ = writer.WriteField("needPercentile", "1")
	_ = writer.WriteField("page", "1")
	_ = writer.WriteField("start", "0")
	_ = writer.WriteField("limit", "-1")
	_ = writer.WriteField("timestampId", "1642680756758_CDR-group-panel")
	_ = writer.WriteField("clientTimezone", "UTC")
	_ = writer.WriteField("clientOsTimezone", "UTC")
	_ = writer.WriteField("timeout", "3600")
	_ = writer.WriteField("check_active_request", "true")
	err = writer.Close()
	if err != nil {
		fmt.Println(err)
		return err
	}
	url = e.vmEndpoint + "/php/model/sql.php"

	headers["Cookie"] = "PHPSESSID=" + vms.SID
	headers["Content-Type"] = writer.FormDataContentType()
	method = "POST"

	response, err := makeHttpRequest(url, method, payload, headers)
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer response.Body.Close()
	var callStatsList CallStats
	decodeJson := json.NewDecoder(response.Body)

	err = decodeJson.Decode(&callStatsList)
	if err != nil {
		fmt.Println(err)
		return err
	}
	for i := 0; i < len(callStatsList.Results); i++ {
		lastSIPresponse := callStatsList.Results[i].LastSIPresponse
		lastSIPresponseNum := strconv.Itoa(callStatsList.Results[i].LastSIPresponseNum)

		count := callStatsList.Results[i].Count
		ch <- prometheus.MustNewConstMetric(
			callStatsReceived, prometheus.GaugeValue, count, lastSIPresponse, lastSIPresponseNum,
		)
	}

	log.Println("Endpoint scraped")
	return nil
}

func makeHttpRequest(url string, method string, payload *bytes.Buffer, headers map[string]string) (*http.Response, error) {

	var err error
	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		fmt.Println(err)
		return &http.Response{}, err
	}
	for key, element := range headers {
		req.Header.Add(key, element)
	}

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return &http.Response{}, err
	}

	return res, nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file, assume env variables are set.")
	}

	flag.Parse()

	voipmonitorEndpoint := os.Getenv("VOIPMONITOR_ENDPOINT")
	voipmonitorUsername := os.Getenv("VOIPMONITOR_USERNAME")
	voipmonitorPassword := os.Getenv("VOIPMONITOR_PASSWORD")

	exporter := NewExporter(voipmonitorEndpoint, voipmonitorUsername, voipmonitorPassword)
	// prometheus.MustRegister(exporter)
	r := prometheus.NewRegistry()
	r.MustRegister(exporter)
	handler := promhttp.HandlerFor(r, promhttp.HandlerOpts{})
	http.Handle(*metricsPath, handler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
             <head><title>Voipmonitor Calls Exporter</title></head>
             <body>
             <h1>Voipmonitor Calls Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
	})
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
