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
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"
)

var (
	ctx    context.Context
	client *github.Client
	owner  = "mihomo-party-org"
	repo   = "mihomo-party"
)

type IssueCloseInfo struct {
	Close   bool   `json:"close"`
	Content string `json:"content"`
}

func init() {
	ctx = context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	tc := oauth2.NewClient(ctx, ts)
	client = github.NewClient(tc)
}

func main() {

	issues := getIssues(ctx)

	for _, issue := range issues {

		c := fmt.Sprintf(`请分析以下 GitHub issue:
标题: "%s"
内容: "%s"
请特别关注 "Verify steps" 部分，并判断用户填写的内容是否满足了这些步骤的要求。如果不满足，请提供一个 JSON 格式的回答，说明关闭该 issue 的理由。`, issue.GetTitle(), issue.GetBody())

		content1, err := chat(c, "gpt-4o-mini")
		if err != nil {
			log.Println(err)
		}
		gpt, err := parse(content1)
		if err != nil {
			log.Println(err)
		}

		content2, err := chat(c, "gemini-1.5-flash")
		if err != nil {
			log.Println(err)
		}
		gemini, err := parse(content2)
		if err != nil {
			log.Println(err)
		}

		content3, err := chat(c, "claude-3-5-sonnet-20240620")
		if err != nil {
			log.Println(err)
		}
		claude, err := parse(content3)
		if err != nil {
			log.Println(err)
		}

		fmt.Println(gpt,
			gemini,
			claude)

		if gpt.Close && gemini.Close && claude.Close {
			closeIssue(issue, claude.Content)
			// fmt.Println("close issue")
		}
	}

}
func getIssues(ctx context.Context) []*github.Issue {
	issues, _, err := client.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
		State: "open",
		ListOptions: github.ListOptions{
			PerPage: 1,
		},
	})
	if err != nil {
		log.Println("Error fetching issues: ", err)
		return nil
	}

	return issues
}

func closeIssue(issue *github.Issue, s string) {
	issueNumber := issue.GetNumber()

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
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
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
		{Role: "system", Content: `You are an AI assistant specialized in analyzing GitHub issues. Your task is to evaluate the title and body of a given issue and determine if they align with the selected options or checkboxes in the issue template, especially focusing on the "Verify steps" section.

Your response must be in JSON format only, with no additional text. The JSON should contain two fields:
1. "close": a boolean indicating whether the issue should be closed (true) or not (false).
2. "content": a string in Chinese explaining the reason for the decision.

Example response format:
{
"close": true,
"content": "该 issue 被关闭，因为验证步骤未完成。具体原因：..."
}

Consider all available information, not just the checkboxes. 

If there's not enough information to make a determination, state that in your response.

If the content involves abusive or inappropriate language, please close the issues`,
		},
		{Role: "user", Content: s},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := client.CreateChatCompletion(ctx, m, messages)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return "", err
	}

	return response.Choices[0].Message.Content, nil
}
