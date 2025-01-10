package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/caarlos0/env"
	"github.com/joho/godotenv"
	pdf "github.com/loxiouve/unipdf/v3/model"
	"github.com/pterm/pterm"
	"github.com/sirupsen/logrus"
)

type EnvSchema struct {
	BOOK_ID    string `env:"BOOK_ID"`
	START_PAGE int    `env:"START_PAGE"`
}

type Job struct {
	uri   string
	index int
}

var (
	environment EnvSchema
	log         = logrus.New()

	numWorkers   int       = 10
	fetchingDone chan bool = make(chan bool, 1)
	updates      chan bool = make(chan bool, numWorkers)
	wg           sync.WaitGroup
	bar          *pterm.ProgressbarPrinter

	workerCtx, workerCancel = context.WithCancelCause(context.Background())
)

const (
	DATA_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/data.js"
	PAGE_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/media/%s"
)

func init() {
	log.Formatter = &logrus.TextFormatter{FullTimestamp: true}
	log.Level = logrus.InfoLevel
}

func loadEnv() {
	err := godotenv.Load(".env")
	if err != nil {
		log.WithError(err).Fatal("Failed to load .env file")
	}

	err = env.Parse(&environment)
	if err != nil {
		log.WithError(err).Fatal("Failed to parse environment variables")
	}
}

func findPageFileNames() ([]string, error) {
	dataUri := fmt.Sprintf(DATA_URI_PATTERN, environment.BOOK_ID)

	response, err := http.Get(dataUri)
	if err != nil {
		log.WithError(err).Error("Failed to fetch data")
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		log.WithError(err).Error("Failed to read data")
		return nil, err
	}

	bodyString := string(body)
	pdfPattern := regexp.MustCompile(`page-([0-9a-z]*)\.pdf`)
	matches := pdfPattern.FindAllString(bodyString, -1)

	log.WithField("page_count", len(matches)).Info("Found page file names")
	return matches, nil
}

func fetchPage(pageUri string, index int) (*pdf.PdfPage, error) {
	response, err := http.Get(pageUri)
	if err != nil {
		bar.Stop()
		log.WithFields(logrus.Fields{"index": index, "uri": pageUri}).Error("Failed to fetch page")
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		bar.Stop()
		log.WithFields(logrus.Fields{"index": index, "uri": pageUri}).Error("Failed to read page")
		return nil, err
	}

	body = bytes.Replace(body, []byte("%ADF-1.6"), []byte("%PDF-1.6"), 1)
	bodyBytes := bytes.NewReader(body)

	currentPdf, err := pdf.NewPdfReader(bodyBytes)
	if err != nil {
		bar.Stop()
		log.WithFields(logrus.Fields{"index": index, "uri": pageUri}).Error("Failed to read PDF")
		return nil, err
	}

	page, err := currentPdf.GetPage(1)
	if err != nil {
		bar.Stop()
		log.WithFields(logrus.Fields{"index": index, "uri": pageUri}).Error("Failed to get page from PDF")
		return nil, err
	}
	return page, nil
}

var isExiting bool = false

func worker(jobs <-chan Job, results []*pdf.PdfPage) {
	defer wg.Done()
	if workerCtx.Err() != nil {
		return
	}
	for {
		select {
		case <-workerCtx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			page, err := fetchPage(job.uri, job.index)
			if err != nil {
				if !isExiting {
					log.WithFields(logrus.Fields{
						"uri": job.uri, "index": job.index,
					}).Errorf("Detecting error, exiting..")
				}
				isExiting = true
				workerCancel(err)
				return
			}
			results[job.index] = page
			updates <- true
		}
	}
}

func download() error {
	pageFileNames, err := findPageFileNames()
	if err != nil {
		return err
	}

	pdfWriter := pdf.NewPdfWriter()
	numPages := len(pageFileNames) - environment.START_PAGE

	jobs := make(chan Job, numPages)
	results := make([]*pdf.PdfPage, numPages)

	pterm.DefaultSection.Println("Task Progress")

	bar, _ = pterm.DefaultProgressbar.WithTotal(numPages).Start()

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(jobs, results)
	}

	go func() {
		defer func() { fetchingDone <- true }()
		for {
			select {
			case <-workerCtx.Done():
				{
					return
				}
			case _, ok := <-updates:
				{
					if !ok {
						return
					}
					bar.Add(1)
				}
			}
		}
	}()

	// Enqueue jobs
	for index, pageFilename := range pageFileNames[environment.START_PAGE:] {
		pageUri := fmt.Sprintf(PAGE_URI_PATTERN, environment.BOOK_ID, pageFilename)
		jobs <- Job{uri: pageUri, index: index}
	}
	close(jobs)

	// Wait for workers to finish
	wg.Wait()
	close(updates)
	<-fetchingDone
	bar.Stop()
	if workerCtx.Err() != nil {
		log.WithError(context.Cause(workerCtx)).Fatal("Error during fetching")
	}

	// Write pages to PDF
	log.Info("Writing pages to PDF")
	for _, result := range results {
		pdfWriter.AddPage(result)
	}

	// Save PDF
	err = os.MkdirAll("output", os.ModePerm)
	if err != nil {
		log.WithError(err).Error("Failed to create output directory")
		return err
	}

	outputFilePath := "output/downloaded.pdf"
	outputIO, err := os.Create(outputFilePath)
	if err != nil {
		log.WithError(err).Error("Failed to create output file")
		return err
	}
	defer outputIO.Close()

	err = pdfWriter.Write(outputIO)
	if err != nil {
		log.WithError(err).Error("Failed to write PDF")
		return err
	}

	log.WithField("file", outputFilePath).Info("Successfully downloaded PDF")
	return nil
}

func main() {
	loadEnv()
	err := download()
	if err != nil {
		log.WithError(err).Fatal("Download process failed")
	}
}
