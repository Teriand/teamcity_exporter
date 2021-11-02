package main

import (
//	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	tc "github.com/teriand/teamcity-go-bindings"
	"github.com/orcaman/concurrent-map"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"

//	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

const (
	namespace = "teamcity"
)

var metricsStorage = cmap.New()

var (
	instanceStatus = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "instance_status"),
		"Teamcity instance status",
		[]string{"instance"}, nil,
	)
	instanceLastScrapeFinishTime = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "instance_last_scrape_finish_time"),
		"Teamcity instance last scrape finish time",
		[]string{"instance"}, nil,
	)
	instanceLastScrapeDuration = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "instance_last_scrape_duration"),
		"Teamcity instance last scrape duration",
		[]string{"instance"}, nil,
	)
)

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	prometheus.MustRegister(version.NewCollector("teamcity_exporter"))
}

func main() {
	var (
		//showVersion   = flag.Bool("version", false, "Print version information")
		listenAddress = kingpin.Flag("web.listen-address", "Address to listen on for web interface and telemetry").Default(":9107").String()
		metricsPath   = kingpin.Flag("web.telemetry-path",  "Path under which to expose metrics").Default("/metrics").String()
		configPath    = kingpin.Flag("config", "Path to configuration file").Default("config.yaml").String()
	)
	//flag.Parse()
	
	promlogConfig := &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, promlogConfig)
	kingpin.Version("1.1.1")
	kingpin.CommandLine.UsageWriter(os.Stdout)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(promlogConfig)
	
	level.Info(logger).Log("Starting teamcity_exporter", version.Info())
	level.Info(logger).Log("Build context", version.BuildContext())

	collector := NewCollector()
	prometheus.MustRegister(collector)

	config := Configuration{}
	if err := config.parseConfig(*configPath); err != nil {
		level.Error(logger).Log("Failed to parse configuration file: %v", err)
	}
	if err := config.validateConfig(); err != nil {
		level.Error(logger).Log("Failed to validate configuration: %v", err)
	}

	for i := range config.Instances {
		go config.Instances[i].collectStat()
	}

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
					 <head><title>Teamcity Exporter</title></head>
					 <body>
					 <h1>Teamcity Exporter</h1>
					 <p><a href='` + *metricsPath + `'>Metrics</a></p>
					 </body>
					 </html>`))
	})
	level.Info(logger).Log("Listening on", *listenAddress)
	level.Error(logger).Log(http.ListenAndServe(*listenAddress, nil))
}

func (i *Instance) collectStat() {
	client := tc.New(i.URL, i.Username, i.Password, i.ConcurrencyLimit)

	ticker := newTicker(time.Duration(i.ScrapeInterval) * time.Second)
	for _ = range ticker.c {
		err := i.validateStatus(client)
		if err != nil {
			//log.Error(err)
			fmt.Printf("collectStat %q", err)
			continue
		}
		go i.collectStatHandlerNew(client)
		//go i.collectStatHandler(client)
	}
}
func (i *Instance) collectStatHandlerNew(client *tc.Client) {
	startProcessing := time.Now()
	
	chBuilds := make(chan Build)
	
	
	wg2 := new(sync.WaitGroup)
	if len(i.BuildsFilters) != 0 {
		for _, bf := range i.BuildsFilters {
			wg2.Add(1)
			go func(f BuildFilter) {
				defer wg2.Done()
				f.instance = i.Name
				//tmp,_ := strconv.Atoi(f.Filter.SinceDate)
				currentTime := time.Now()
				adjustTime := currentTime.Add(-time.Second * time.Duration(i.ScrapeInterval))
				f.Filter.SinceDate = fmt.Sprintf("%s%%2b0300",adjustTime.Format("20060102T150405"))
				fmt.Printf("collectStatHandlerNew %s", f.Filter.SinceDate)
				builds, err := client.GetLatestBuildNew(f.Filter)
				if err != nil {
					fmt.Printf("collectStatHandlerNew %q", err)
					return
				}
				for i := range builds.Builds {
					chBuilds <- Build{Details: builds.Builds[i], Filter: f}
				}
			}(bf)
		}
	}
	wg2.Wait()
	close(chBuilds)
	finishProcessing := time.Now()
	metricsStorage.Set(getHash(instanceLastScrapeFinishTime.String(), i.Name), prometheus.MustNewConstMetric(instanceLastScrapeFinishTime, prometheus.GaugeValue, float64(finishProcessing.Unix()), i.Name))
	metricsStorage.Set(getHash(instanceLastScrapeDuration.String(), i.Name), prometheus.MustNewConstMetric(instanceLastScrapeDuration, prometheus.GaugeValue, time.Since(startProcessing).Seconds(), i.Name))
}
func (i *Instance) collectStatHandler(client *tc.Client) {
	startProcessing := time.Now()

	chBuilds := make(chan Build)
	chBuildsStat := make(chan BuildStatistics)

	wg1 := new(sync.WaitGroup)
	wg1.Add(2)
	go parseStat(wg1, chBuildsStat)
	go getBuildStat(client, wg1, chBuilds, chBuildsStat)

	wg2 := new(sync.WaitGroup)
	if len(i.BuildsFilters) != 0 {
		for _, bf := range i.BuildsFilters {
			wg2.Add(1)
			go func(f BuildFilter) {
				defer wg2.Done()
				f.instance = i.Name
				builds, err := client.GetLatestBuild(f.Filter)
				if err != nil {
					fmt.Printf("collectStatHandler %q", err)
					return
				}
				for i := range builds.Builds {
					chBuilds <- Build{Details: builds.Builds[i], Filter: f}
				}
			}(bf)
		}
	} else {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			f := BuildFilter{instance: i.Name, Name: "<default>"}
			builds, err := client.GetLatestBuild(f.Filter)
			if err != nil {
				fmt.Printf("collectStatHandler %q", err)
				return
			}
			for i := range builds.Builds {
				chBuilds <- Build{Details: builds.Builds[i], Filter: f}
			}
		}()
	}
	wg2.Wait()
	close(chBuilds)

	wg1.Wait()
	finishProcessing := time.Now()
	metricsStorage.Set(getHash(instanceLastScrapeFinishTime.String(), i.Name), prometheus.MustNewConstMetric(instanceLastScrapeFinishTime, prometheus.GaugeValue, float64(finishProcessing.Unix()), i.Name))
	metricsStorage.Set(getHash(instanceLastScrapeDuration.String(), i.Name), prometheus.MustNewConstMetric(instanceLastScrapeDuration, prometheus.GaugeValue, time.Since(startProcessing).Seconds(), i.Name))
}

func getBuildStat(c *tc.Client, wg *sync.WaitGroup, chIn <-chan Build, chOut chan<- BuildStatistics) {
	defer wg.Done()
	wg1 := &sync.WaitGroup{}
	for i := range chIn {
		wg1.Add(1)
		go func(i Build) {
			defer wg1.Done()
			s, err := c.GetBuildStat(i.Details.ID)
			if err != nil {
				//log.Errorf("Failed to query build statistics for build %s: %v", i.Details.WebURL, err)
				fmt.Printf("getBuildStat %q", err)
				return
			}
			chOut <- BuildStatistics{Build: i, Stat: s}
		}(i)
	
		title := fmt.Sprint(namespace, "_build_info")
	labels := []Label{
		{"exporter_instance", i.Filter.instance},
		{"exporter_filter", i.Filter.Name},
		{"build_configuration", string(i.Details.BuildTypeID)},
		{"branch", i.Details.BranchName},
		{"id", string(strconv.Itoa(int(i.Details.ID)))},
		{"number", i.Details.Number},
//		{"status", i.Details.Status},
		{"state", i.Details.State},
		{"url", i.Details.WebURL},
		}
	labelsTitles, labelsValues := []string{}, []string{}
	for v := range labels {
		labelsTitles = append(labelsTitles, labels[v].Name)
		labelsValues = append(labelsValues, labels[v].Value)
	}
	var v float64 = 0.0
	if (i.Details.Status == "SUCCESS") {
		v = 1.0
	}else if (i.Details.Status == "FAILURE") {
		v = 3.0
	}
	desc := prometheus.NewDesc(title, title, labelsTitles, nil)
	metricsStorage.Set(getHash(title, labelsValues...), prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labelsValues...))
	}
	wg1.Wait()
	close(chOut)
}

func parseStat(wg *sync.WaitGroup, chIn <-chan BuildStatistics) {
	defer wg.Done()

	for i := range chIn {
		for k := range i.Stat.Property {
			value, err := strconv.ParseFloat(i.Stat.Property[k].Value, 64)
			if err != nil {
				//log.Errorf("Failed to convert string '%s' to float: %v", i.Stat.Property[k].Value, err)
				fmt.Printf("%q", err)
				continue
			}
			metric := strings.SplitN(i.Stat.Property[k].Name, ":", 2)
			title := fmt.Sprint(namespace, "_", toSnakeCase(metric[0]))

			labels := []Label{
				{"exporter_instance", i.Build.Filter.instance},
				{"exporter_filter", i.Build.Filter.Name},
				{"build_configuration", string(i.Build.Details.BuildTypeID)},
				{"branch", i.Build.Details.BranchName},
				{"id", string(strconv.Itoa(int(i.Build.Details.ID)))},
				{"number", i.Build.Details.Number},
//				{"status", i.Build.Details.Status},
				{"state", i.Build.Details.State},
				{"url", i.Build.Details.WebURL},
			}
			if len(metric) > 1 {
				labels = append(labels, Label{"other", metric[1]})
			}

			labelsTitles, labelsValues := []string{}, []string{}
			for v := range labels {
				labelsTitles = append(labelsTitles, labels[v].Name)
				labelsValues = append(labelsValues, labels[v].Value)
			}

			desc := prometheus.NewDesc(title, title, labelsTitles, nil)
			metricsStorage.Set(getHash(title, labelsValues...), prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labelsValues...))
		}
	}
}

func (i *Instance) validateStatus(client *tc.Client) error {
	req, err := http.NewRequest("GET", i.URL, nil)
	if err != nil {
		metricsStorage.Set(getHash(instanceStatus.String(), i.Name), prometheus.MustNewConstMetric(instanceStatus, prometheus.GaugeValue, 0, i.Name))
		return err
	}

	resp, err := client.HTTPClient.Do(req)
	defer resp.Body.Close()
	if err != nil {
		metricsStorage.Set(getHash(instanceStatus.String(), i.Name), prometheus.MustNewConstMetric(instanceStatus, prometheus.GaugeValue, 0, i.Name))
		return err
	}

	if resp.StatusCode == 401 {
		req.SetBasicAuth(i.Username, i.Password)
		resp, err = client.HTTPClient.Do(req)
	}
	defer resp.Body.Close()

	if err != nil {
		metricsStorage.Set(getHash(instanceStatus.String(), i.Name), prometheus.MustNewConstMetric(instanceStatus, prometheus.GaugeValue, 0, i.Name))
		return err
	}

	if resp.StatusCode == 401 {
		metricsStorage.Set(getHash(instanceStatus.String(), i.Name), prometheus.MustNewConstMetric(instanceStatus, prometheus.GaugeValue, 0, i.Name))
		return fmt.Errorf("Unauthorized instance '%s'", i.Name)
	}
	metricsStorage.Set(getHash(instanceStatus.String(), i.Name), prometheus.MustNewConstMetric(instanceStatus, prometheus.GaugeValue, 1, i.Name))
	return nil
}
