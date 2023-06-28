package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	"github.com/sirupsen/logrus"
	"github.com/vbauerster/mpb/v8"

	"github.com/cenkalti/backoff/v4"
	pinecone "github.com/nekomeowww/go-pinecone"
	openai "github.com/sashabaranov/go-openai"
	gptparallel "github.com/tbiehn/gptparallel"
)

func expandHomeDir(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal("Path has ~/ in it - but unable to resolve UserHomeDir()", err)
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

type Metadata struct {
	Filename  string
	StartByte int
	EndByte   int
	Directory string
}

// EmbeddingData represents the structure of the JSON data input.
type EmbeddingData struct {
	EmbeddingType  string   `json:"EmbeddingType"`
	Metadata       Metadata `json:"Metadata"`
	Summary        string   `json:"Summary"`
	OriginalText   string   `json:"OriginalText"`
	PrependedText  string   `json:"PrependedText"`
	ProcessingText string   `json:"ProcessingText"`
}

type EmbeddingResponse struct {
	Input    any `json:"Input"`
	Response any `json:"Response"`
}

var (
	mode                string // Mode of operation: "upsert" or "retrieve"
	indexName           string // Pinecone index name
	accountRegion       string // Pinecone account region
	projectName         string // Pinecone project name
	pineconeAPIKey      string // Pinecone API key
	topK                int    // TopK parameter for retrieval
	embeddingsDirectory string //Where to store / retrieve embedding files.
	pineNamespace       string //Where to store / retrieve embedding files.

)
var log = logrus.New()

func setupLogger(logLevel string) {
	// Set log output to stderr and set the log level
	log.Out = os.Stderr

	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		log.Fatalf("Invalid log level: %v", err)
	}

	log.SetLevel(level)

	// Customize log format
	log.SetFormatter(&logrus.TextFormatter{})
}

type CommandLineFlags struct {
	embedParam     string
	maxTokens      int
	configPath     string
	dryRun         bool
	logLevel       string
	disableBar     bool
	concurrency    int
	azureEndpoint  string
	azureModelName string
}

var flags CommandLineFlags
var encoding, _ = tiktoken.EncodingForModel("gpt-3.5-turbo")

func main() {

	flag.StringVar(&mode, "mode", "upsert", "Mode of operation: upsert, retrieve, or deleteAll")
	flag.IntVar(&flags.maxTokens, "tokens", 8191, "Recursive bisection split input if it exceeds this many tokens.s")
	flag.StringVar(&flags.embedParam, "param", "search", "Name of JSON string object to compute embedding for.")

	flag.StringVar(&indexName, "index", "", "Pinecone index name")
	flag.StringVar(&pineNamespace, "namespace", "", "Index namespace")
	flag.StringVar(&embeddingsDirectory, "edir", "~/.embedmeup/embeddings/", "Where to store the raw embedding content.")

	flag.StringVar(&accountRegion, "region", "", "Pinecone account region")
	flag.StringVar(&projectName, "project", "", "Pinecone project name")
	flag.IntVar(&topK, "topK", 10, "TopK parameter for retrieval")
	//flag.BoolVar(&flags.dryRun, "d", false, "d[ry] Perform a dry run, calculating token usage without making a request. Set -d all by itself to enable it.")
	flag.StringVar(&flags.logLevel, "l", "info", "l[og] level (options: debug, info, warn, error, fatal, panic)")      // Keep this flag for log level
	flag.BoolVar(&flags.disableBar, "b", false, "b[ar] Disable the progress bar. Set -b all by itself to disable it.") // Keep this flag to disable the progress bar
	flag.IntVar(&flags.concurrency, "p", 10, "p[arallel] How many parallel calls to make to OpenAI.")
	flag.StringVar(&flags.azureEndpoint, "ae", "", "a[zure]e[ndpoint] Set if using Azure. Your OpenAI HTTP Endpoint. Set environment variable 'AZUREAI_API_KEY' to your API key.")

	flag.Parse()

	//unimplemented.
	flags.dryRun = false

	setupLogger(flags.logLevel)

	var aiclient *openai.Client

	pineconeAPIKey := os.Getenv("PINECONE_API_KEY")
	if pineconeAPIKey == "" {
		log.Fatalf("PINECONE_API_KEY environment variable not set")
	}

	if flags.azureEndpoint != "" {

		azureAPIKey := os.Getenv("AZUREAI_API_KEY")
		if azureAPIKey == "" {
			log.Fatalf("AZUREAI_API_KEY environment variable not set")
		}

		config := openai.DefaultAzureConfig(azureAPIKey, flags.azureEndpoint)
		aiclient = openai.NewClientWithConfig(config)
	} else {
		openAIKey := os.Getenv("OPENAI_API_KEY")
		if openAIKey == "" {
			log.Fatalf("OPENAI_API_KEY environment variable not set")
		}

		aiclient = openai.NewClient(os.Getenv("OPENAI_API_KEY"))
	}

	ctx := context.Background()

	canonDir, err := filepath.Abs(expandHomeDir(embeddingsDirectory))
	embeddingsDirectory = canonDir
	if err != nil {
		log.Fatal("Could not canonicalize path.")
	}
	log.Infof("Storing embedding chunks in %s", embeddingsDirectory)
	// Create '.embeddings' directory if it doesn't exist
	if _, err := os.Stat(embeddingsDirectory); os.IsNotExist(err) {
		err = os.MkdirAll(embeddingsDirectory, 0755)
		if err != nil {
			log.Fatalf("Couldn't create embeddings directory. %s", err)
		}
	}
	// Initialize Pinecone client
	client, err := pinecone.NewIndexClient(
		pinecone.WithIndexName(indexName),
		pinecone.WithEnvironment(accountRegion),
		pinecone.WithProjectName(projectName),
		pinecone.WithAPIKey(pineconeAPIKey),
	)
	if err != nil {
		log.Fatal(err)
	}

	params := pinecone.DescribeIndexStatsParams{}
	resp, err := client.DescribeIndexStats(ctx, params)
	if err != nil {
		panic(err)
	}
	log.Info("Connected to pinecone.")
	for k, v := range resp.Namespaces {
		log.Infof("Index Namespace: %s: %+v\n", k, v)
	}

	backoffSettings := backoff.NewExponentialBackOff()

	var g *gptparallel.GPTParallel
	var gptResultsChan <-chan gptparallel.VectorRequestResult

	bar := mpb.New(mpb.WithOutput(log.Out)) // Keep this line to redirect the mpb output to stderr

	if flags.disableBar {
		bar = nil // Set to nil if the progress bar is disabled
	}

	requestsChan := make(chan gptparallel.VectorRequestWithCallback, flags.concurrency*1000)

	if flags.dryRun {
		gptResultsChan = make(chan gptparallel.VectorRequestResult)
	} else {
		g = gptparallel.NewGPTParallel(ctx, aiclient, bar, backoffSettings, log)
		gptResultsChan = g.RunEmbeddingsChan(requestsChan, flags.concurrency)
		//Drain response channel.
		go func() {
			for {
				<-gptResultsChan
			}
		}()
	}

	// Switch operation based on mode
	switch mode {
	case "upsert":
		err := upsertEmbeddings(client, requestsChan)
		if err != nil {
			log.Fatal(err)
		}
	case "retrieve":
		dec := json.NewDecoder(os.Stdin)
		for {
			var data map[string]any
			if err := dec.Decode(&data); err == io.EOF {
				break
			} else if err != nil {
				log.Fatal("error decoding JSON: %w", err)
			}

			search, found := data[flags.embedParam]
			if !found {
				log.Fatal("Input didn't contain a search parameter.")
			}
			searchStr, ok := search.(string)
			if !ok {
				log.Fatal("Parameter ", flags.embedParam, " is not a string.")
			}
			vector, err := computeEmbedding(searchStr, requestsChan)
			if err != nil {
				log.Fatal(err)
			}

			err, jsonData := retrieveEmbeddings(client, vector)
			if err != nil {
				log.Fatal(err)
			}

			resp := &EmbeddingResponse{
				Input:    data,
				Response: jsonData,
			}

			jsonStr, err := json.Marshal(resp)
			if err != nil {
				log.Fatal("error marshaling JSON: %w", err)
			}
			fmt.Println(string(jsonStr))

		}
	case "deleteAll":
		ctx := context.Background()
		params := pinecone.DeleteVectorsParams{
			DeleteAll: true,
		}
		if pineNamespace != "" {
			params.Namespace = pineNamespace
		}
		err := client.DeleteVectors(ctx, params)

		if err != nil {
			log.Fatalf("error querying vectors from Pinecone: %w", err)
		}
		log.Info("Deleted all vectors")

	default:
		log.Fatalf("Unknown mode: %s", mode)
	}
}
func upsertEmbeddingsChunkAware(client *pinecone.IndexClient, embedClient chan gptparallel.VectorRequestWithCallback) error {
	ctx := context.Background()

	var wg sync.WaitGroup

	// Read JSON from stdin
	dec := json.NewDecoder(os.Stdin)
	for {
		var data EmbeddingData
		if err := dec.Decode(&data); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("error decoding JSON: %w", err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Compute embedding
			embedding, err := computeEmbedding(data.ProcessingText, embedClient)
			if err != nil {
				log.Errorf("error computing embedding: %w", err)
				return
			}

			// Compute SHA-256 hash of JSON data and use it as ID
			jsonData, err := json.Marshal(data)
			if err != nil {
				log.Fatal("error marshaling JSON: %w", err)
			}
			hash := sha256.Sum256(jsonData)
			id := fmt.Sprintf("%x", hash)

			// Store JSON data in a file named with its SHA-256 hash
			filename := filepath.Join(embeddingsDirectory, id)
			if err := ioutil.WriteFile(filename, jsonData, 0644); err != nil {
				log.Fatal("error writing JSON to file: %w", err)
			}

			// Upsert embedding to Pinecone
			params := pinecone.UpsertVectorsParams{
				Vectors: []*pinecone.Vector{
					{
						ID:     id,
						Values: embedding,
						Metadata: map[string]any{
							"Directory": data.Metadata.Directory,
							"Filename":  data.Metadata.Filename,
							//"StartByte": data.Metadata.StartByte,
							//"EndByte":   data.Metadata.EndByte,
							"Type": data.EmbeddingType},
					},
				},
			}
			if pineNamespace != "" {
				params.Namespace = pineNamespace
			}
			if _, err := client.UpsertVectors(ctx, params); err != nil {
				log.Fatal("error upserting vectors to Pinecone: %w", err)
			}

		}()
	}
	wg.Wait()
	return nil
}

func upsertEmbeddings(client *pinecone.IndexClient, embedClient chan gptparallel.VectorRequestWithCallback) error {
	ctx := context.Background()

	var wg sync.WaitGroup

	// Read JSON from stdin
	dec := json.NewDecoder(os.Stdin)
	for {
		var data map[string]any
		if err := dec.Decode(&data); err == io.EOF {
			break
		} else if err != nil {
			log.Fatal("error decoding JSON: %w", err)
		}

		search, found := data[flags.embedParam]
		if !found {
			log.Fatal("Input didn't contain the embedding parameter ", flags.embedParam, ".")
		}
		searchStr, ok := search.(string)
		if !ok {
			log.Fatal("Parameter ", flags.embedParam, " is not a string.")
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			chunks := make([]string, 0)

			if len(encoding.Encode(searchStr, nil, nil)) > flags.maxTokens {
				chunks = bisectSplitTokens(searchStr, flags.maxTokens)
				log.Debugln("Split input ", searchStr, "into chunks;")
				for _, chunk := range chunks {
					log.Debugln("Chunk:", chunk)
				}
			} else {
				chunks = append(chunks, searchStr)
			}

			for _, chunk := range chunks {
				// Compute embedding
				embedding, err := computeEmbedding(chunk, embedClient)
				if err != nil {
					log.Errorf("error computing embedding: %w", err)
					//Skip malformed input.
					return
				}

				data[flags.embedParam] = chunk

				// Compute SHA-256 hash of JSON data and use it as ID
				jsonData, err := json.Marshal(data)
				if err != nil {
					log.Fatal("error marshaling JSON: %w", err)
				}
				hash := sha256.Sum256(jsonData)
				id := fmt.Sprintf("%x", hash)

				// Store JSON data in a file named with its SHA-256 hash
				filename := filepath.Join(embeddingsDirectory, id)
				if err := ioutil.WriteFile(filename, jsonData, 0644); err != nil {
					log.Fatal("error writing JSON to file: %w", err)
				}

				// Upsert embedding to Pinecone
				params := pinecone.UpsertVectorsParams{
					Vectors: []*pinecone.Vector{
						{
							ID:     id,
							Values: embedding,
							/*Metadata: map[string]any{
							"Directory": data.Metadata.Directory,
							"Filename":  data.Metadata.Filename,
							//"StartByte": data.Metadata.StartByte,
							//"EndByte":   data.Metadata.EndByte,
							"Type": data.EmbeddingType},*/
						},
					},
				}
				if pineNamespace != "" {
					params.Namespace = pineNamespace
				}
				if _, err := client.UpsertVectors(ctx, params); err != nil {
					log.Fatal("error upserting vectors to Pinecone: %w", err)
				}

			}

		}()
	}
	wg.Wait()
	return nil
}

func computeEmbedding(text string, embedClient chan gptparallel.VectorRequestWithCallback) ([]float32, error) {
	var result []float32
	var wg sync.WaitGroup

	//Embedding computation fails on empty strings.
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("Empty string requested for embedding.")
	}

	wg.Add(1)

	embedClient <- gptparallel.VectorRequestWithCallback{
		Request: openai.EmbeddingRequest{
			Input: []string{text},
			Model: openai.AdaEmbeddingV2,
		},
		Callback: func(inresult gptparallel.VectorRequestResult) {
			defer wg.Done()
			result = inresult.Vector
		},
		Identifier: text,
	}
	wg.Wait()

	if len(result) == 0 {
		return nil, fmt.Errorf("Problem processing vector for input [" + text + "]")
	}

	return result, nil
}
func retrieveEmbeddings(client *pinecone.IndexClient, vector []float32) (error, any) {
	ctx := context.Background()

	// Query vectors from Pinecone
	params := pinecone.QueryParams{
		Vector: vector,
		TopK:   int64(topK),
	}
	if pineNamespace != "" {
		params.Namespace = pineNamespace
	}
	resp, err := client.Query(ctx, params)

	if err != nil {
		return fmt.Errorf("error querying vectors from Pinecone: %w", err), nil
	}

	// Collect the retrieved vectors and associated stored data
	var jsonData []interface{}
	for _, result := range resp.Matches {
		// Read stored data from file
		filename := filepath.Join(embeddingsDirectory, result.ID)
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			log.Errorf("error reading data from file: %w", err)
			continue
		}

		// Convert to JSON and add to jsonData
		var jsonObject interface{}
		if err := json.Unmarshal(data, &jsonObject); err != nil {
			return fmt.Errorf("error unmarshaling JSON: %w", err), nil
		}
		jsonData = append(jsonData, jsonObject)

	}

	return nil, jsonData
}

func bisectSplitTokens(text string, targetTokenCount int) []string {
	lines := strings.Split(text, "\n")
	var chunks []string
	currentChunk := ""
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		newLine := "\n" + trimmedLine
		if currentChunk == "" {
			newLine = trimmedLine
		}
		newTokenCount := len(encoding.Encode(currentChunk+newLine, nil, nil))
		if newTokenCount <= targetTokenCount {
			currentChunk += newLine
		} else {
			chunks = append(chunks, currentChunk)
			currentChunk = trimmedLine
		}
	}
	if currentChunk != "" {
		chunks = append(chunks, currentChunk)
	}

	var bisectedChunks []string
	for _, chunk := range chunks {
		if len(encoding.Encode(chunk, nil, nil)) > targetTokenCount {
			words := strings.Split(chunk, " ")
			if len(words) > 1 {
				mid := len(words) / 2
				bisectedChunks = append(bisectedChunks, bisectSplitTokens(strings.Join(words[:mid], " "), targetTokenCount)...)
				bisectedChunks = append(bisectedChunks, bisectSplitTokens(strings.Join(words[mid:], " "), targetTokenCount)...)
			} else {
				mid := len(chunk) / 2
				bisectedChunks = append(bisectedChunks, bisectSplitTokens(chunk[:mid], targetTokenCount)...)
				bisectedChunks = append(bisectedChunks, bisectSplitTokens(chunk[mid:], targetTokenCount)...)
			}
		} else {
			bisectedChunks = append(bisectedChunks, chunk)
		}
	}
	return bisectedChunks
}
