package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/caarlos0/env"
	"github.com/joho/godotenv"
	pdf "github.com/loxiouve/unipdf/v3/model"
	"github.com/sirupsen/logrus"
)

type EnvSchema struct {
	BOOK_ID    string `env:"BOOK_ID"`
	START_PAGE int    `env:"START_PAGE"`
}

var (
	environment EnvSchema
	log         = logrus.New()
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

const DATA_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/data.js"
const PAGE_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/media/%s"

func findPageFileNames() ([]string, error) {
	dataUri := fmt.Sprintf(DATA_URI_PATTERN, environment.BOOK_ID)
	log.WithField("url", dataUri).Info("Fetching data")

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

func fetchPage(pageUri string) (*pdf.PdfPage, error) {
	log.WithField("url", pageUri).Info("Fetching page")

	response, err := http.Get(pageUri)
	if err != nil {
		log.WithError(err).Error("Failed to fetch page")
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		log.WithError(err).Error("Failed to read page")
		return nil, err
	}

	body = bytes.Replace(body, []byte("%ADF-1.6"), []byte("%PDF-1.6"), 1)
	bodyBytes := bytes.NewReader(body)

	currentPdf, err := pdf.NewPdfReader(bodyBytes)
	if err != nil {
		log.WithError(err).Error("Failed to read PDF")
		return nil, err
	}

	page, err := currentPdf.GetPage(1)
	if err != nil {
		log.WithError(err).Error("Failed to get page from PDF")
		return nil, err
	}

	log.WithField("page_number", currentPdf.GetNumPages).Info("Successfully fetched page")
	return page, nil
}

func worker(jobs <-chan string, results chan<- *pdf.PdfPage, errors chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	for pageUri := range jobs {
		page, err := fetchPage(pageUri)
		if err != nil {
			errors <- err
			continue
		}
		results <- page
	}
}

func download() error {
	pageFileNames, err := findPageFileNames()
	if err != nil {
		return err
	}

	numWorkers := 10
	pdfWriter := pdf.NewPdfWriter()

	jobs := make(chan string, len(pageFileNames)-environment.START_PAGE)
	results := make(chan *pdf.PdfPage, len(pageFileNames)-environment.START_PAGE)
	errors := make(chan error, len(pageFileNames)-environment.START_PAGE)

	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			worker((<-chan string)(jobs), chan<- *pdf.PdfPage(results), chan<- error(errors), &wg)
		}()
	}

	// Enqueue jobs
	for _, pageFilename := range pageFileNames[environment.START_PAGE:] {
		pageUri := fmt.Sprintf(PAGE_URI_PATTERN, environment.BOOK_ID, pageFilename)
		jobs <- pageUri
	}
	close(jobs)

	// Wait for workers to finish
	go func() {
		wg.Wait()
		close(results)
		close(errors)
	}()

	for i := 0; i < len(pageFileNames)-environment.START_PAGE; i++ {
		select {
		case err := <-errors:
			log.WithError(err).Error("Error while fetching pages")
		case page := <-results:
			pdfWriter.AddPage(page)
		}
	}

	log.Info("Writing pages to PDF")
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
