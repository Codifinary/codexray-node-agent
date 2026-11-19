// Copyright Codexray
// Derived from coroot/coroot-node-agent (https://github.com/coroot/coroot-node-agent).
// SPDX-License-Identifier: Apache-2.0

package prom

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codifinary/codexray-node-agent/common"
	"github.com/codifinary/codexray-node-agent/flags"
	"github.com/golang/snappy"
	"github.com/jpillora/backoff"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/util/fmtutil"
	"k8s.io/klog/v2"
)

const (
	RemoteWriteTimeout = 30 * time.Second

	// memoryBufferMaxBytes caps the payloads kept in memory while the spool cannot be written.
	memoryBufferMaxBytes = 32 << 20

	// metricsQuarantineMaxBytes caps the spool files the collector rejected for good.
	metricsQuarantineMaxBytes = 50 << 20
	quarantineDirName         = "quarantine"

	rejectedBodyLogBytes = 512
	maxDrainedBodyBytes  = 64 << 10
)

// remoteWriteError is a non-2xx answer from the collector.
type remoteWriteError struct {
	status string
	code   int
	body   string
}

func (e *remoteWriteError) Error() string {
	if e.body == "" {
		return e.status
	}
	return e.status + ": " + e.body
}

// permanentRejection reports whether retrying the payload can never succeed.
func permanentRejection(err error) (*remoteWriteError, bool) {
	var rejected *remoteWriteError
	if errors.As(err, &rejected) && common.ClassifyHTTPStatus(rejected.code) == common.SendPermanent {
		return rejected, true
	}
	return nil, false
}

type Agent struct {
	reg          *prometheus.Registry
	url          *url.URL
	labels       map[string]string
	sourceLabels map[string]string
	sources      []SeriesSource

	httpClient http.Client

	spoolDir     string
	maxSpoolSize int64

	// Payloads scraped while the spool could not be written (e.g. a full disk), oldest first.
	// They are sent from memory so node metrics keep flowing, and moved to the spool once it recovers.
	memMu        sync.Mutex
	memBuf       []memPayload
	memBufBytes  int
	maxMemBytes  int
	spoolFailing atomic.Bool

	metrics *agentMetrics
}

type memPayload struct {
	timestamp int64
	data      []byte
}

type agentMetrics struct {
	spoolHealthy      prometheus.Gauge
	memoryBufferBytes prometheus.Gauge
	droppedPayloads   prometheus.Counter
	rejectedPayloads  *prometheus.CounterVec
}

func newAgentMetrics(reg prometheus.Registerer) *agentMetrics {
	m := &agentMetrics{
		spoolHealthy:      prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "metrics", Name: "spool_healthy", Help: "Whether the on-disk metrics spool can be written."}),
		memoryBufferBytes: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "node_agent", Subsystem: "metrics", Name: "memory_buffer_bytes", Help: "Bytes of metrics payloads kept in memory because the spool could not be written."}),
		droppedPayloads:   prometheus.NewCounter(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "metrics", Name: "dropped_payloads_total", Help: "Metrics payloads dropped because the in-memory buffer was full."}),
		rejectedPayloads:  prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "node_agent", Subsystem: "metrics", Name: "rejected_payloads_total", Help: "Metrics payloads the collector rejected for good, by HTTP status."}, []string{"code"}),
	}
	reg.MustRegister(m.spoolHealthy, m.memoryBufferBytes, m.droppedPayloads, m.rejectedPayloads)
	m.spoolHealthy.Set(1)
	return m
}

func (m *agentMetrics) setSpoolHealthy(healthy bool) {
	if m == nil {
		return
	}
	if healthy {
		m.spoolHealthy.Set(1)
	} else {
		m.spoolHealthy.Set(0)
	}
}

func (m *agentMetrics) setMemoryBufferBytes(n int) {
	if m != nil {
		m.memoryBufferBytes.Set(float64(n))
	}
}

func (m *agentMetrics) incDroppedPayloads() {
	if m != nil {
		m.droppedPayloads.Inc()
	}
}

func (m *agentMetrics) incRejectedPayloads(code int) {
	if m != nil {
		m.rejectedPayloads.WithLabelValues(strconv.Itoa(code)).Inc()
	}
}

// SeriesSource supplies transient application metrics that must be committed
// only after the combined remote-write payload is safely in the disk spool.
type SeriesSource interface {
	Snapshot(maxSeries int) []prompb.TimeSeries
	Commit(count int)
}

// StartAgent starts scraping and remote-writing metrics. It returns nil when no metrics endpoint is configured.
func StartAgent(reg *prometheus.Registry, machineId, systemUuid string, sources ...SeriesSource) *Agent {
	if *flags.MetricsEndpoint == nil {
		return nil
	}
	klog.Infoln("metrics remote write endpoint:", (*flags.MetricsEndpoint).String())

	up := prometheus.NewGauge(prometheus.GaugeOpts{Name: "up"})
	up.Set(1)
	reg.MustRegister(up)

	sourceLabels := SourceLabels(machineId, systemUuid)
	labels := map[string]string{
		model.InstanceLabel: sourceLabels[model.InstanceLabel],
		model.JobLabel:      sourceLabels[model.JobLabel],
	}

	a := &Agent{
		reg:          reg,
		url:          *flags.MetricsEndpoint,
		labels:       labels,
		sourceLabels: sourceLabels,
		sources:      append([]SeriesSource(nil), sources...),
		httpClient: http.Client{
			Timeout: RemoteWriteTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: *flags.InsecureSkipVerify},
			},
		},
		spoolDir:     path.Join(*flags.WalDir, "spool"),
		maxSpoolSize: int64(*flags.MaxSpoolSize),
		maxMemBytes:  memoryBufferMaxBytes,
		metrics:      newAgentMetrics(reg),
	}
	// A full disk must not stop the agent (and its logs) from running: until the spool
	// directory can be created, metrics are kept in memory and writeToSpool retries it.
	if err := os.MkdirAll(a.spoolDir, 0750); err != nil {
		klog.Warningln("failed to create the metrics spool directory, metrics will be kept in memory:", err)
		a.setSpoolFailing(true)
	}
	go a.sendLoop()
	go a.scrapeLoop()
	return a
}

// Healthy reports whether metrics can be written to the on-disk spool.
// A nil Agent (metrics disabled) is healthy.
func (a *Agent) Healthy() bool {
	return a == nil || !a.spoolFailing.Load()
}

func (a *Agent) setSpoolFailing(failing bool) {
	if a.spoolFailing.Swap(failing) && !failing {
		klog.Infoln("metrics spool can be written again")
	}
	a.metrics.setSpoolHealthy(!failing)
}

// SourceLabels returns the protected identity labels shared by node telemetry
// and independently-spooled custom metrics.
func SourceLabels(machineId, systemUuid string) map[string]string {
	instance := machineId
	if s := strings.ReplaceAll(systemUuid, "-", ""); s != "" && s != machineId {
		hash := md5.New()
		hash.Write([]byte(machineId))
		hash.Write([]byte(s))
		instance = hex.EncodeToString(hash.Sum(nil))
	}
	labels := map[string]string{
		model.InstanceLabel: instance,
		model.JobLabel:      "codexray-node-agent",
	}
	if machineId != "" {
		labels["machine_id"] = machineId
	}
	if systemUuid != "" {
		labels["system_uuid"] = systemUuid
	}
	return labels
}

func (a *Agent) scrapeLoop() {
	if err := a.scrape(); err != nil {
		klog.Warningln("failed to scrape metrics:", err)
	}
	ticker := time.NewTicker(*flags.ScrapeInterval)
	for range ticker.C {
		if err := a.scrape(); err != nil {
			klog.Warningln("failed to scrape metrics:", err)
		}
	}
}

func (a *Agent) sendLoop() {
	b := backoff.Backoff{Factor: 2, Min: 5 * time.Second, Max: time.Minute}
	for {
		sent, err := a.sendOnce()
		if err != nil {
			dur := b.Duration()
			klog.Warningf(
				"failed to send metrics to %s, next attempt in %s: %s",
				a.url, dur.String(), err,
			)
			time.Sleep(dur)
			continue
		}
		if !sent {
			time.Sleep(5 * time.Second)
			continue
		}
		b.Reset()
	}
}

// sendOnce sends the oldest pending payload: spool files first, then payloads kept in
// memory while the spool could not be written. It reports whether anything was pending.
func (a *Agent) sendOnce() (bool, error) {
	fName, err := a.getOldestSpoolFile()
	if err != nil && !a.spoolFailing.Load() {
		klog.Warningln("failed to get oldest spool file:", err)
	}
	if fName != "" {
		if err := a.send(fName); err != nil {
			if rejected, ok := permanentRejection(err); ok {
				return true, a.quarantine(fName, rejected)
			}
			return true, err
		}
		// The spool may have evicted the file while it was being sent.
		if err := os.Remove(fName); err != nil && !os.IsNotExist(err) {
			return true, err
		}
		return true, nil
	}
	p, ok := a.takeOldestFromMemory()
	if !ok {
		return false, nil
	}
	if err := a.post(bytes.NewReader(p.data)); err != nil {
		if rejected, ok := permanentRejection(err); ok {
			a.metrics.incRejectedPayloads(rejected.code)
			klog.Errorf("collector rejected a buffered metrics payload, dropping it: %s", rejected)
			return true, nil
		}
		a.putBackOldestInMemory(p)
		return true, err
	}
	return true, nil
}

// quarantine moves a spool file the collector rejected for good out of the queue, so the files after it are sent.
func (a *Agent) quarantine(fName string, rejected *remoteWriteError) error {
	a.metrics.incRejectedPayloads(rejected.code)
	dest, err := quarantineSpoolFile(fName, path.Join(a.spoolDir, quarantineDirName), rejected.code, metricsQuarantineMaxBytes)
	switch {
	case err == nil:
		klog.Errorf("collector rejected %s with %s, moved to %s", fName, rejected, dest)
		return nil
	case os.IsNotExist(err):
		return nil
	}
	klog.Errorf("collector rejected %s with %s and it could not be quarantined (%s), deleting it", fName, rejected, err)
	if err := os.Remove(fName); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting rejected spool file %s: %w", fName, err)
	}
	return nil
}

// quarantineSpoolFile moves src into dir, then deletes the oldest quarantined files while dir holds more than maxBytes.
func quarantineSpoolFile(src, dir string, code int, maxBytes int64) (string, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	dest := path.Join(dir, fmt.Sprintf("%s.%d.failed", path.Base(src), code))
	if err := os.Rename(src, dest); err != nil {
		return "", err
	}
	return dest, trimQuarantine(dir, maxBytes)
}

func trimQuarantine(dir string, maxBytes int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	sizes := map[string]int64{}
	var total int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".failed") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := path.Join(dir, e.Name())
		files = append(files, name)
		sizes[name] = info.Size()
		total += info.Size()
	}
	sort.Strings(files)
	for _, name := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= sizes[name]
	}
	return nil
}

func (a *Agent) send(fPath string) error {
	f, err := os.Open(fPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return a.post(f)
}

func (a *Agent) post(body io.Reader) error {
	req, err := http.NewRequest(http.MethodPost, a.url.String(), body)
	if err != nil {
		return err
	}
	for k, v := range common.AuthHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "codexray-node-agent")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	t := time.Now()
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, rejectedBodyLogBytes))     // a cut-off body is still worth logging
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainedBodyBytes)) // drained so the connection is reused
		return &remoteWriteError{status: resp.Status, code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	klog.Infof("sent metrics in %s", time.Since(t).Truncate(time.Millisecond))
	return nil
}

func (a *Agent) scrape() error {
	timestamp := time.Now().UnixNano() / int64(time.Millisecond)

	mfs, err := a.reg.Gather()
	if err != nil {
		return err
	}
	mfsByName := make(map[string]*dto.MetricFamily)
	for _, mf := range mfs {
		mfsByName[mf.GetName()] = mf
	}
	wr := buildWriteRequest(mfs, timestamp, a.labels)
	nodeSeries := len(wr.Timeseries)
	type sourceSnapshot struct {
		source SeriesSource
		count  int
	}
	snapshots := make([]sourceSnapshot, 0, len(a.sources))
	for _, source := range a.sources {
		if source == nil {
			continue
		}
		series := source.Snapshot(0)
		if len(series) == 0 {
			continue
		}
		for i := range series {
			wr.Timeseries = append(wr.Timeseries, withExternalLabels(series[i], a.sourceLabels))
		}
		snapshots = append(snapshots, sourceSnapshot{source: source, count: len(series)})
	}
	decompressed, err := wr.Marshal()
	if err != nil {
		return err
	}

	compressed := snappy.Encode(nil, decompressed)
	if err = a.writeToSpool(timestamp, compressed); err != nil {
		a.setSpoolFailing(true)
		payload := compressed
		if len(snapshots) > 0 {
			// External series are committed only once they are safely on disk, so they stay
			// with their source and the in-memory copy carries only the agent's own metrics.
			nodeOnly := prompb.WriteRequest{Timeseries: wr.Timeseries[:nodeSeries], Metadata: wr.Metadata}
			raw, marshalErr := nodeOnly.Marshal()
			if marshalErr != nil {
				return marshalErr
			}
			payload = snappy.Encode(nil, raw)
		}
		a.bufferInMemory(timestamp, payload)
		return fmt.Errorf("spool write failed, keeping metrics in memory: %w", err)
	}
	a.setSpoolFailing(false)
	a.flushMemoryToSpool()
	for _, snapshot := range snapshots {
		snapshot.source.Commit(snapshot.count)
	}
	return nil
}

// bufferInMemory keeps a payload that could not be spooled. Over the byte limit the oldest
// payloads are dropped, but the newest one is always kept.
func (a *Agent) bufferInMemory(timestamp int64, data []byte) {
	a.memMu.Lock()
	defer a.memMu.Unlock()
	a.memBuf = append(a.memBuf, memPayload{timestamp: timestamp, data: data})
	a.memBufBytes += len(data)
	a.trimMemoryLocked()
}

func (a *Agent) takeOldestFromMemory() (memPayload, bool) {
	a.memMu.Lock()
	defer a.memMu.Unlock()
	if len(a.memBuf) == 0 {
		return memPayload{}, false
	}
	p := a.memBuf[0]
	a.memBuf[0] = memPayload{}
	a.memBuf = a.memBuf[1:]
	a.memBufBytes -= len(p.data)
	a.metrics.setMemoryBufferBytes(a.memBufBytes)
	return p, true
}

// putBackOldestInMemory returns a payload taken by takeOldestFromMemory that could not be delivered.
func (a *Agent) putBackOldestInMemory(p memPayload) {
	a.memMu.Lock()
	defer a.memMu.Unlock()
	a.memBuf = append([]memPayload{p}, a.memBuf...)
	a.memBufBytes += len(p.data)
	a.trimMemoryLocked()
}

func (a *Agent) trimMemoryLocked() {
	limit := a.maxMemBytes
	if limit <= 0 {
		limit = memoryBufferMaxBytes
	}
	for len(a.memBuf) > 1 && a.memBufBytes > limit {
		a.memBufBytes -= len(a.memBuf[0].data)
		a.memBuf[0] = memPayload{}
		a.memBuf = a.memBuf[1:]
		a.metrics.incDroppedPayloads()
	}
	a.metrics.setMemoryBufferBytes(a.memBufBytes)
}

// flushMemoryToSpool moves payloads kept in memory to the spool under their original
// timestamps, so the send loop delivers them in order. It stops at the first failure.
func (a *Agent) flushMemoryToSpool() {
	for {
		p, ok := a.takeOldestFromMemory()
		if !ok {
			return
		}
		if err := a.writeToSpool(p.timestamp, p.data); err != nil {
			a.putBackOldestInMemory(p)
			klog.Warningln("failed to move buffered metrics to the spool:", err)
			return
		}
	}
}

func withExternalLabels(series prompb.TimeSeries, agentLabels map[string]string) prompb.TimeSeries {
	labels := make(map[string]string, len(series.Labels)+len(agentLabels))
	for _, label := range series.Labels {
		labels[label.Name] = label.Value
	}
	// Agent identity always wins over application-provided labels.
	for name, value := range agentLabels {
		labels[name] = value
	}

	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	result := prompb.TimeSeries{
		Labels:  make([]prompb.Label, 0, len(names)),
		Samples: append([]prompb.Sample(nil), series.Samples...),
	}
	for _, name := range names {
		result.Labels = append(result.Labels, prompb.Label{Name: name, Value: labels[name]})
	}
	return result
}

func (a *Agent) writeToSpool(timestamp int64, payload []byte) error {
	if err := os.MkdirAll(a.spoolDir, 0750); err != nil {
		return err
	}
	if err := a.truncateSpoolIfNeeded(); err != nil {
		return err
	}
	fileName := fmt.Sprintf("spool-%d.done", timestamp)
	f, err := os.CreateTemp(a.spoolDir, fileName)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if _, err = f.Write(payload); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path.Join(a.spoolDir, fileName)); err != nil {
		return err
	}
	return nil
}

func (a *Agent) truncateSpoolIfNeeded() error {
	files, err := a.listSpoolFiles()
	if err != nil {
		return err
	}
	if len(files) <= 1 {
		return nil
	}
	totalSize := int64(0)
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return err
		}
		totalSize += st.Size()
	}
	if totalSize > a.maxSpoolSize {
		klog.Warningln("spool size exceeded, removing the oldest file:", files[0])
		if err = os.Remove(files[0]); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) listSpoolFiles() ([]string, error) {
	entities, err := os.ReadDir(a.spoolDir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entities))
	for _, e := range entities {
		name := e.Name()
		if strings.HasPrefix(name, "spool-") && strings.HasSuffix(name, ".done") {
			files = append(files, path.Join(a.spoolDir, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

func (a *Agent) getOldestSpoolFile() (string, error) {
	files, err := a.listSpoolFiles()
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	return files[0], nil
}

func makeLabelsMap(m *dto.Metric, metricName string, extraLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(m.Label)+len(extraLabels)+2) //1 for name, 1 for possible le
	labels[model.MetricNameLabel] = metricName
	for key, value := range extraLabels {
		labels[key] = value
	}
	for _, label := range m.Label {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

func makeLabels(labelsMap map[string]string, metricNameSuffix, bucket string) []prompb.Label {
	l := len(labelsMap)
	if bucket != "" {
		l++
	}
	sortedLabelNames := make([]string, 0, l)
	for label := range labelsMap {
		sortedLabelNames = append(sortedLabelNames, label)
	}
	if bucket != "" {
		sortedLabelNames = append(sortedLabelNames, model.BucketLabel)
	}
	sort.Strings(sortedLabelNames)
	labels := make([]prompb.Label, len(sortedLabelNames))

	var name, value string
	var i int
	for i, name = range sortedLabelNames {
		value = labelsMap[name]
		switch name {
		case model.MetricNameLabel:
			if metricNameSuffix != "" {
				value += metricNameSuffix
			}
		case model.BucketLabel:
			value = bucket
		}
		labels[i].Name = name
		labels[i].Value = value
	}
	return labels
}

func buildWriteRequest(mfs []*dto.MetricFamily, timestamp int64, extraLabels map[string]string) *prompb.WriteRequest {
	wr := &prompb.WriteRequest{}
	for _, mf := range mfs {
		if len(mf.Metric) == 0 {
			continue
		}
		mtype := fmtutil.MetricMetadataTypeValue[mf.Type.String()]
		mName := mf.GetName()
		metadata := prompb.MetricMetadata{
			MetricFamilyName: mName,
			Type:             prompb.MetricMetadata_MetricType(mtype),
			Help:             mf.GetHelp(),
		}
		wr.Metadata = append(wr.Metadata, metadata)

		for _, metric := range mf.Metric {
			addTimeseries(wr, metric, makeLabelsMap(metric, mName, extraLabels), timestamp)
		}
	}
	return wr
}

func addTimeseries(wr *prompb.WriteRequest, m *dto.Metric, labels map[string]string, timestamp int64) {
	switch {
	case m.Gauge != nil:
		wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
			Samples: []prompb.Sample{{
				Timestamp: timestamp,
				Value:     m.GetGauge().GetValue(),
			}},
			Labels: makeLabels(labels, "", ""),
		})
	case m.Counter != nil:
		wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
			Samples: []prompb.Sample{{
				Timestamp: timestamp,
				Value:     m.GetCounter().GetValue(),
			}},
			Labels: makeLabels(labels, "", ""),
		})
	case m.Histogram != nil:
		for _, b := range m.GetHistogram().Bucket {
			wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
				Samples: []prompb.Sample{{Timestamp: timestamp, Value: float64(b.GetCumulativeCount())}},
				Labels:  makeLabels(labels, "_bucket", fmt.Sprint(b.GetUpperBound())),
			})
		}
		wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
			Samples: []prompb.Sample{{Timestamp: timestamp, Value: float64(m.Histogram.GetSampleCount())}},
			Labels:  makeLabels(labels, "_bucket", "+Inf"),
		})
		wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
			Samples: []prompb.Sample{{Timestamp: timestamp, Value: m.GetHistogram().GetSampleSum()}},
			Labels:  makeLabels(labels, "_sum", ""),
		})
		wr.Timeseries = append(wr.Timeseries, prompb.TimeSeries{
			Samples: []prompb.Sample{{Timestamp: timestamp, Value: float64(m.GetHistogram().GetSampleCount())}},
			Labels:  makeLabels(labels, "_count", ""),
		})
	}
}
