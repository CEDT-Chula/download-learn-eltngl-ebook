package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/caarlos0/env"
	"github.com/joho/godotenv"
	pdf "github.com/loxiouve/unipdf/v3/model"
)

type EnvSchema struct {
	BOOK_ID    string `env:"BOOK_ID"`
	START_PAGE int    `env:"START_PAGE"`
}

var environment EnvSchema

func loadEnv() {
	err := godotenv.Load(`.env`)
	_ = env.Parse(&environment)
	if err != nil {
		log.Fatalf("Error loading .env file")
	}
}

const DATA_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/data.js"
const PAGE_URI_PATTERN = "https://learn.eltngl.com/cdn_proxy/%s/media/%s"

func findPageFileNames() ([]string, error) {
	dataUri := fmt.Sprintf(DATA_URI_PATTERN, environment.BOOK_ID)
	response, err := http.Get(dataUri)
	if err != nil {
		log.Fatalln(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	bodyString := string(body)
	pdfPattern := regexp.MustCompile(`page-([0-9a-z]*)\.pdf`)
	matches := pdfPattern.FindAllString(bodyString, -1)
	return matches, nil
}

func fetchPage(pageUri string) (*pdf.PdfPage, error) {
	response, err := http.Get(pageUri)
	if err != nil {
		fmt.Println("response")
		return nil, err
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		fmt.Println("body")
		return nil, err
	}

	body = bytes.Replace(body, []byte("%ADF-1.6"), []byte("%PDF-1.6"), 1)
	bodyBytes := bytes.NewReader(body)

	currentPdf, err := pdf.NewPdfReader(bodyBytes)
	if err != nil {
		fmt.Println("currentPdf")
		return nil, err
	}

	page, err := currentPdf.GetPage(1)
	if err != nil {
		return nil, err
	}
	return page, nil
}

func download() error {
	pageFileNames, err := findPageFileNames()
	if err != nil {
		return err
	}
	pdfWriter := pdf.NewPdfWriter()
	pagesList := make([]*pdf.PdfPage, len(pageFileNames)-environment.START_PAGE)
	var wg sync.WaitGroup
	for index, pageFilename := range pageFileNames[environment.START_PAGE:] {
		pageUri := fmt.Sprintf(PAGE_URI_PATTERN, environment.BOOK_ID, pageFilename)

		wg.Add(1)
		go func(index int, pageUri string) error {
			page, err := fetchPage(pageUri)
			if err != nil {
				return err
			}
			pagesList[index] = page
			wg.Done()
			return nil
		}(index, pageUri)
	}

	wg.Wait()

	for _, page := range pagesList {
		pdfWriter.AddPage(page)
	}

	err = os.MkdirAll("output", os.ModePerm)
	if err != nil {
		return err
	}
	outputIO, err := os.Create("output/downloaded.pdf")
	if err != nil {
		return err
	}
	defer outputIO.Close()
	err = pdfWriter.Write(outputIO)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	loadEnv()
	err := download()
	if err != nil {
		fmt.Println("error: ", err)
	}
}
