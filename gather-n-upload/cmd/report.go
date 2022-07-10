/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"io/ioutil"
	"path/filepath"
	"time"

	_ "embed"

	"cloud.google.com/go/storage"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
	"k8s.io/utils/strings/slices"
)

//go:embed templates/content.html
var content_template string

// reportCmd represents the report command
var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "command to pull log report",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		var report Report
		loc, _ := time.LoadLocation("Asia/Jakarta")
		end := time.Now().In(loc)
		start := end.Add(-24 * time.Hour)
		outputList := []string{"json", "html", "pdf"}
		output, _ := cmd.Flags().GetString("output")
		outputdir, _ := cmd.Flags().GetString("output-dir")
		bucketname, _ := cmd.Flags().GetString("log-bucket")
		report.Created = end.Format("2006-01-02 15:04:05.000000")

		if !slices.Contains(outputList, output) {
			log.Fatalf("output type %s not found, try [json, html, pdf]", output)
		}

		objectList, err := getObjectList("", "", bucketname, start, end)
		if err != nil {
			log.Errorf("error : %w", err)
		}

		eventList, err := getEventLogList(objectList, bucketname)
		if err != nil {
			log.Errorf("error : %w", err)
		}

		report.EventList = eventList

		err = createReport(output, outputdir, report, end)
		if err != nil {
			log.Errorf("error : %w", err)
		}
	},
}

func createReport(output string, outputDir string, report Report, now time.Time) error {
	var err error
	switch output {
	case "json":
		file, err := json.MarshalIndent(report, "", "")
		if err != nil {
			return err
		}

		err = write(outputDir, file, now, output)
		if err != nil {
			return err
		}

	case "html":
		var rendered bytes.Buffer
		template, err := template.New("html").Parse(content_template)
		if err != nil {
			log.Errorf("error : %w", err)
		}
		template.ExecuteTemplate(&rendered, "log", report)

		err = write(outputDir, rendered.Bytes(), now, output)
		if err != nil {
			return err
		}

	case "pdf":
	}

	return err
}

func write(outputDir string, data []byte, now time.Time, output string) error {
	var err error

	errIsDirectoryMsg := "open " + filepath.Join(outputDir) + ": is a directory"
	err = ioutil.WriteFile(outputDir, data, 0664)
	if err != nil {
		if err.Error() == errIsDirectoryMsg {
			newPath := filepath.Join(outputDir, now.Format("2006-01-02 15:04:05.000000"+"."+output))
			ioutil.WriteFile(newPath, data, 0664)
		} else {
			return err
		}
	}

	return err
}

func getObjectList(prefix string, delim string, bucket string, start time.Time, end time.Time) ([]string, error) {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second*50)
	defer cancel()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	var objects []string

	it := client.Bucket(bucket).Objects(ctx, &storage.Query{
		Prefix:    prefix,
		Delimiter: delim,
	})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Fatalf("Bucket(%q).Objects(): %v", bucket, err)
			return nil, err
		}
		if attrs.Created.After(start) && attrs.Created.Before(end) {
			objects = append(objects, attrs.Name)
		}
	}

	return objects, nil
}

func getEventLogList(objectList []string, bucket string) ([]EventLog, error) {
	var eventList []EventLog
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second*50)
	defer cancel()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return eventList, err
	}
	defer client.Close()

	for _, object := range objectList {
		var tempEventLog EventLog
		rc, err := client.Bucket(bucket).Object(object).NewReader(ctx)
		if err != nil {
			log.Errorf("Object(%q).NewReader: %v", object, err.Error())
			return eventList, err
		}
		defer rc.Close()

		data, err := ioutil.ReadAll(rc)
		if err != nil {
			return eventList, err
		}

		err = json.Unmarshal(data, &tempEventLog)
		if err != nil {
			return eventList, err
		}

		eventList = append(eventList, tempEventLog)
	}

	return eventList, err
}

func init() {
	rootCmd.AddCommand(reportCmd)

	reportCmd.Flags().StringP("output", "o", "json", "output type [json, html, pdf]")
	reportCmd.Flags().StringP("output-dir", "", ".", "path to directory")
	reportCmd.Flags().StringP("log-bucket", "", "", "log bucket name")

	reportCmd.MarkFlagRequired("log-bucket")
}
