package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	orderedmap "github.com/wk8/go-ordered-map/v2"
	"golang.org/x/oauth2"
)

var (
	ctx           context.Context
	client        *github.Client
	owner         string
	repo          string
	issue         IssueInfo
	prompt        string
	models        []string
	comment_model string
)

type IssueInfo struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Number int    `json:"number"`
}

type Contents struct {
	Data *orderedmap.OrderedMap[string, IssueCloseInfo]
}

type IssueCloseInfo struct {
	Close   bool   `json:"close"`
	Lock    bool   `json:"lock"`
	Content string `json:"content"`
}

func init() {
	ctx = context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	tc := oauth2.NewClient(ctx, ts)
	client = github.NewClient(tc)
	githubRepo := strings.Split(os.Getenv("GITHUB_REPO"), "/")
	owner, repo = githubRepo[0], githubRepo[1]
	issueNumber, err := strconv.Atoi(os.Getenv("ISSUE_NUMBER"))
	if err != nil {
		log.Fatalln(err)
	}
	issue = IssueInfo{
		Title:  os.Getenv("ISSUE_TITLE"),
		Body:   os.Getenv("ISSUE_BODY"),
		Number: issueNumber,
	}
	prompt = os.Getenv("SYSTEM_PROMPT")

	modelsEnv := strings.ReplaceAll(os.Getenv("MODELS"), " ", "")
	if modelsEnv != "" {
		models = strings.Split(modelsEnv, ",")
	}
	if len(models) == 0 {
		models = []string{"gpt-4o", "gpt-4o-mini"}
	}
	comment_model = os.Getenv("COMMENT_MODEL")
}

func main() {

	c := fmt.Sprintf(`请分析以下 GitHub issue:
	标题: "%s"
	内容: "%s"`, issue.Title, issue.Body)
	close := true
	lock := true
	contents := Contents{
		Data: orderedmap.New[string, IssueCloseInfo](),
	}

	for _, model := range models {
		content, err := chat(c, model)
		if err != nil {
			log.Fatalln(err)
			continue
		}
		parsedContent, err := parse(content)
		if err != nil {
			log.Fatalln(err)
			continue
		}
		contents.Data.Set(model, *parsedContent)

		if !parsedContent.Close {
			close = false
		}
		if !parsedContent.Lock {
			lock = false
		}
		log.Printf(`
Model: %s
Content: %s
Close: %t
Lock: %t
`, model, parsedContent.Content, parsedContent.Close, parsedContent.Lock)
	}

	if contents.Data.Len() != 0 {

		if close {
			var comment string
			if pair := contents.Data.Oldest(); pair != nil {
				comment = pair.Value.Content
			}
			if comment_model != "" {
				for pair := contents.Data.Oldest(); pair != nil; pair = pair.Next() {
					if pair.Key == comment_model {
						comment = pair.Value.Content
						break
					}
				}
			}
			closeIssue(issue.Number, comment)
		}
		if lock {
			lockIssue(issue.Number)
		}
	}
}

func closeIssue(issueNumber int, s string) {
	comment := &github.IssueComment{
		Body: github.String(s),
	}
	_, _, err := client.Issues.CreateComment(ctx, owner, repo, issueNumber, comment)
	if err != nil {
		log.Printf("Error adding comment to issue #%d: %v", issueNumber, err)
		return
	}

	issueRequest := &github.IssueRequest{
		State:       github.String("closed"),
		StateReason: github.String("not_planned"),
	}

	_, _, err = client.Issues.Edit(ctx, owner, repo, issueNumber, issueRequest)
	if err != nil {
		log.Printf("Error closing issue #%d: %v", issueNumber, err)
		return
	}

	fmt.Printf("Closed issue #%d\n", issueNumber)
}

func lockIssue(issueNumber int) {
	_, err := client.Issues.Lock(ctx, owner, repo, issueNumber, &github.LockIssueOptions{})
	if err != nil {
		log.Printf("Error locking issue #%d: %v", issueNumber, err)
		return
	}

	fmt.Printf("Locked issue #%d\n", issueNumber)
}

func parse(s string) (*IssueCloseInfo, error) {
	jsonstr, err := extractJSONs(s)

	var issueCloseInfo IssueCloseInfo
	for _, j := range jsonstr {
		err = json.Unmarshal([]byte(j), &issueCloseInfo)
		if err == nil {
			return &issueCloseInfo, nil
		}
	}
	return nil, err
}

func extractJSONs(text string) ([]string, error) {
	re := regexp.MustCompile(`\{[^{}]*\}`)
	matches := re.FindAllString(text, -1)

	var validJSONs []string

	for _, match := range matches {
		trimmed := strings.TrimSpace(match)

		if json.Valid([]byte(trimmed)) {
			validJSONs = append(validJSONs, trimmed)
		}
	}

	if len(validJSONs) == 0 {
		return nil, fmt.Errorf("no valid JSON objects found")
	}

	return validJSONs, nil
}

type OpenAIClient struct {
	BaseURL    string
	APIKey     string
	HttpClient *http.Client
	Cache      *sync.Map
}

type ChatCompletionRequest struct {
	Model          string         `json:"model"`
	Messages       []ChatMessage  `json:"messages"`
	ResponseFormat ResponseFormat `json:"response_format"`
}

type ResponseFormat struct {
	Type       string     `json:"type"`
	JsonSchema JsonSchema `json:"json_schema"`
}

type JsonSchema struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema Schema `json:"schema"`
}

type Schema struct {
	Type                 string     `json:"type"`
	Properties           Properties `json:"properties"`
	Required             []string   `json:"required"`
	AdditionalProperties bool       `json:"additionalProperties"`
}

type Properties struct {
	Close   Close   `json:"close"`
	Lock    Lock    `json:"lock"`
	Content Content `json:"content"`
}

type Close struct {
	Type string `json:"type"`
}

type Lock struct {
	Type string `json:"type"`
}

type Content struct {
	Type string `json:"type"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Role    string `json:"role"`
			Refusal bool   `json:"refusal"`
		}
	} `json:"choices"`
}

func NewOpenAIClient(baseURL, apiKey string) *OpenAIClient {
	return &OpenAIClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HttpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
			Timeout: 30 * time.Second,
		},
		Cache: &sync.Map{},
	}
}

func (c *OpenAIClient) CreateChatCompletion(ctx context.Context, model string, messages []ChatMessage) (*ChatCompletionResponse, error) {
	reqBody := ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		ResponseFormat: ResponseFormat{
			Type: "json_schema",
			JsonSchema: JsonSchema{
				Name:   "response",
				Strict: true,
				Schema: Schema{
					Type: "object",
					Properties: Properties{
						Close: Close{
							Type: "boolean",
						},
						Lock: Lock{
							Type: "boolean",
						},
						Content: Content{
							Type: "string",
						},
					},
					Required:             []string{"close", "lock", "content"},
					AdditionalProperties: false,
				},
			},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var reader io.ReadCloser
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
	default:
		reader = resp.Body
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var result ChatCompletionResponse
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func chat(s string, m string) (string, error) {
	client := NewOpenAIClient(os.Getenv("API_URL"), os.Getenv("API_KEY"))

	messages := []ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: s},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := client.CreateChatCompletion(ctx, m, messages)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return "", err
	}

	if response.Choices[0].Message.Refusal {
		return "", fmt.Errorf("Refusal")
	}

	return response.Choices[0].Message.Content, nil
}
