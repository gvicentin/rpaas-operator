// Copyright 2020 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"
	"time"

	sprig "github.com/Masterminds/sprig/v3"
	"github.com/olekukonko/tablewriter"
	"github.com/urfave/cli/v2"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/tsuru/rpaas-operator/api/v1alpha1"
	rpaasclient "github.com/tsuru/rpaas-operator/pkg/rpaas/client"
	clientTypes "github.com/tsuru/rpaas-operator/pkg/rpaas/client/types"
)

func NewCmdInfo() *cli.Command {
	return &cli.Command{
		Name:  "info",
		Usage: "Shows an information summary about an instance",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "service",
				Aliases: []string{"tsuru-service", "s"},
				Usage:   "the Tsuru service name",
			},
			&cli.StringFlag{
				Name:     "instance",
				Aliases:  []string{"tsuru-service-instance", "i"},
				Usage:    "the reverse proxy instance name",
				Required: true,
			},
			&cli.BoolFlag{
				Name:    "raw-output",
				Aliases: []string{"r", "raw"},
				Usage:   "show as JSON instead of the predefined format",
				Value:   false,
			},
		},
		Before: setupClient,
		Action: runInfo,
	}
}

func newFuncMap() template.FuncMap {
	fm := template.FuncMap{
		"formatBlocks":       writeInfoBlocksOnTableFormat,
		"formatRoutes":       writeInfoRoutesOnTableFormat,
		"formatAddresses":    writeAddressesOnTableFormat,
		"formatBinds":        writeBindsOnTableFormat,
		"formatAutoscale":    writeAutoscaleOnTableFormat,
		"formatPods":         writePodsOnTableFormat,
		"formatPodErrors":    writePodErrorsOnTableFormat,
		"formatCertificates": writeCertificatesOnTableFormat,
		"formatEvents":       writeEventsOnTableFormat,
		"formatACLs":         writeAccessControlListOnTableFormat,
		"formatExtraFiles":   writeExtraFilesOnTableFormat,
	}

	for k, v := range sprig.HtmlFuncMap() {
		fm[k] = v
	}

	return fm
}

var instanceInfoTemplate = template.Must(template.New("rpaasv2.instance.info").
	Funcs(newFuncMap()).
	Parse(`
{{- $instance := . -}}
Name: {{ .Name }}
{{- if .Dashboard}}
Dashboard: {{ .Dashboard }}
{{- end }}
Description: {{ .Description }}
Tags: {{ join ", " .Tags }}
Team owner: {{ .Team }}
Plan: {{ .Plan }}
Flavors: {{ join ", " .Flavors }}
{{- with .Cluster}}
Cluster: {{ . }}
{{- end }}
{{- with .Pool }}
Pool: {{ . }}
{{- end }}

{{- with .PlanOverride }}{{ "\n" }}
Plan overrides:
{{ mustToPrettyJson . }}
{{- end }}

Pods: (current: {{ len .Pods }}{{ if not .Autoscale }} / desired: {{ .Replicas }}{{ end }})
{{- with .Pods }}
{{ formatPods . }}
{{ formatPodErrors . }}
{{- end }}

{{- with .Autoscale }}
Autoscale:
{{ formatAutoscale . }}
{{- end }}

{{- with .ACLs }}
ACLs:
{{ formatACLs . }}
{{- end }}

{{- with .Binds }}
Binds:
{{ formatBinds . }}
{{- end }}

{{- with .Addresses }}
Addresses:
{{ formatAddresses . }}
{{- end }}

{{- with .Certificates }}
Certificates:
{{ formatCertificates . }}
{{- end }}

{{- with .ExtraFiles }}
Extra files:
{{ formatExtraFiles . }}
{{- end }}

{{- with .Blocks }}
Blocks:
{{ formatBlocks . }}
{{- end }}

{{- with .Routes }}
Routes:
{{ formatRoutes . }}
{{- end }}

{{- with .Events }}
Events:
{{ formatEvents . }}
{{- end }}
{{- /* end template */ -}}
`))

func writePodsOnTableFormat(pods []clientTypes.Pod) string {
	if len(pods) == 0 {
		return ""
	}

	var data [][]string
	hasMetrics := false
	for _, pod := range pods {

		if pod.Metrics != nil {
			hasMetrics = true
		}
	}

	for _, pod := range pods {
		row := []string{
			pod.Name,
			pod.HostIP,
			unifiedStatus(pod.Status, pod.Ready),
			fmt.Sprintf("%d", pod.Restarts),
			translateTimestampSince(pod.CreatedAt.In(time.UTC)),
		}

		if hasMetrics {
			if pod.Metrics != nil {
				row = append(
					row,
					cpuValue(pod.Metrics.CPU),
					memoryValue(pod.Metrics.Memory),
				)
			} else {
				row = append(row, "", "")
			}
		}

		data = append(data, row)
	}

	var buffer bytes.Buffer
	table := tablewriter.NewWriter(&buffer)
	header := []string{"Name", "Host", "Status", "Restarts", "Age"}
	if hasMetrics {
		header = append(header, "CPU", "Memory")
	}

	table.SetHeader(header)
	table.SetAutoWrapText(true)
	table.SetAutoFormatHeaders(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.AppendBulk(data)
	table.Render()

	return buffer.String()
}

func cpuValue(q string) string {
	var cpu string
	qt, err := resource.ParseQuantity(q)
	if err == nil {
		cpu = fmt.Sprintf("%d%%", qt.MilliValue()/10)
	}

	return cpu
}

func memoryValue(q string) string {
	var memory string
	qt, err := resource.ParseQuantity(q)
	if err == nil {
		memory = fmt.Sprintf("%vMi", qt.Value()/(1024*1024))
	}
	return memory
}

func unifiedStatus(status string, ready bool) string {
	if ready {
		return "Ready"
	}
	return status
}

func writePodErrorsOnTableFormat(pods []clientTypes.Pod) string {
	data := [][]string{}
	for _, pod := range pods {
		for _, err := range pod.Errors {
			age := translateTimestampSince(err.Last)
			if err.Count > int32(1) {
				age = fmt.Sprintf("%s (x%d over %s)", age, err.Count, translateTimestampSince(err.First))
			}

			data = append(data, []string{age, pod.Name, err.Message})
		}
	}

	if len(data) == 0 {
		return ""
	}

	var buffer bytes.Buffer
	table := tablewriter.NewWriter(&buffer)
	table.SetHeader([]string{"Age", "Pod", "Message"})
	table.SetAutoWrapText(true)
	table.SetAutoFormatHeaders(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.AppendBulk(data)
	table.Render()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Errors:\n%v", buffer.String()))
	return sb.String()
}

func writeAutoscaleOnTableFormat(autoscale *clientTypes.Autoscale) string {
	var buffer bytes.Buffer
	writeAutoscale(&buffer, autoscale)
	return buffer.String()
}

func writeAddressesOnTableFormat(adresses []clientTypes.InstanceAddress) string {
	data := [][]string{}
	for _, address := range adresses {
		var hostnames strings.Builder
		for _, h := range strings.Split(address.Hostname, ",") {
			if hostnames.Len() != 0 {
				hostnames.WriteByte('\n')
			}

			hostnames.WriteString(h)
		}

		var ips strings.Builder
		for _, ip := range strings.Split(address.IP, ",") {
			if ips.Len() != 0 {
				ips.WriteByte('\n')
			}

			ips.WriteString(ip)
		}

		data = append(data, []string{string(address.Type), hostnames.String(), ips.String(), address.Status})
	}
	var buffer bytes.Buffer
	table := tablewriter.NewWriter(&buffer)
	table.SetHeader([]string{"Type", "Hostname", "IP", "Status"})
	table.SetAutoFormatHeaders(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)
	table.SetAutoWrapText(false)
	table.SetReflowDuringAutoWrap(false)
	table.AppendBulk(data)
	table.Render()
	return buffer.String()
}

func writeInfoBlocksOnTableFormat(blocks []clientTypes.Block) string {
	var buffer bytes.Buffer
	writeBlocksOnTableFormat(&buffer, blocks)
	return buffer.String()
}

func writeInfoRoutesOnTableFormat(routes []clientTypes.Route) string {
	var buffer bytes.Buffer
	writeRoutesOnTableFormat(&buffer, routes)
	return buffer.String()
}

func writeBindsOnTableFormat(binds []v1alpha1.Bind) string {
	data := [][]string{}
	for _, bind := range binds {
		data = append(data, []string{bind.Name, bind.Host})
	}
	var buffer bytes.Buffer
	table := tablewriter.NewWriter(&buffer)
	table.SetHeader([]string{"App", "Address"})
	table.SetRowLine(true)
	table.SetAutoFormatHeaders(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAutoWrapText(true)
	table.SetColumnAlignment([]int{tablewriter.ALIGN_LEFT, tablewriter.ALIGN_CENTER, tablewriter.ALIGN_CENTER, tablewriter.ALIGN_LEFT})
	table.AppendBulk(data)
	table.Render()

	return buffer.String()
}

func writeInfoOnJSONFormat(w io.Writer, payload *clientTypes.InstanceInfo) error {
	message, err := json.MarshalIndent(payload, "", "\t")
	if err != nil {
		return err
	}

	fmt.Fprintln(w, string(message))
	return nil
}

func writeCertificatesOnTableFormat(c []clientTypes.CertificateInfo) string {
	var b bytes.Buffer
	writeCertificatesInfoOnTableFormat(&b, c)
	return b.String()
}

func writeEventsOnTableFormat(events []clientTypes.Event) string {
	data := [][]string{}
	for _, event := range events {
		age := translateTimestampSince(event.Last)
		if event.Count > int32(1) {
			age = fmt.Sprintf("%s (x%d over %s)", age, event.Count, translateTimestampSince(event.First))
		}

		data = append(data, []string{event.Type, event.Reason, age, event.Message})
	}

	var buffer bytes.Buffer
	table := tablewriter.NewWriter(&buffer)
	table.SetHeader([]string{"Type", "Reason", "Age", "Message"})
	table.SetRowLine(true)
	table.SetAutoFormatHeaders(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAutoWrapText(true)
	table.SetColumnAlignment([]int{tablewriter.ALIGN_LEFT, tablewriter.ALIGN_LEFT})
	table.AppendBulk(data)
	table.Render()

	return buffer.String()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func translateTimestampSince(timestamp time.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}

	return duration.HumanDuration(time.Since(timestamp))
}

func runInfo(c *cli.Context) error {
	client, err := getClient(c)
	if err != nil {
		return err
	}

	info := rpaasclient.InfoArgs{
		Instance: c.String("instance"),
		Raw:      c.Bool("raw-output"),
	}

	infoPayload, err := client.Info(c.Context, info)
	if err != nil {
		return err
	}

	if info.Raw {
		return writeInfoOnJSONFormat(c.App.Writer, infoPayload)
	}

	writer := newPagerWriter(c.App.Writer)

	err = instanceInfoTemplate.Execute(writer, infoPayload)
	if err != nil {
		return err
	}

	if pw, ok := writer.(*pagerWriter); ok {
		if pw.pagerPipe != nil {
			pw.pagerPipe.Close()
		}
		err = pw.Wait()
		if err != nil {
			return err
		}
	}

	return nil
}
