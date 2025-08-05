package main

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/mattn/go-mastodon"
	"github.com/urfave/cli/v3"
	"golang.org/x/net/html"
)

type RippleBot struct {
	httpClient      *http.Client
	mastodonClient  *mastodon.Client
	anthropicClient *anthropic.Client
}

//go:embed ripple_bot_prompt.md
var systemPrompt string

func renderNode(n *html.Node, buf *bytes.Buffer) {
	if n.Type == html.TextNode {
		buf.WriteString(n.Data)
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(c, buf)
	}
}

func stripTags(htmlStr string) string {
	doc, err := html.Parse(bytes.NewReader([]byte(htmlStr)))
	if err != nil {
		return ""
	}

	var buf bytes.Buffer
	renderNode(doc, &buf)
	return buf.String()
}

func shouldRespondToPost(status *mastodon.Status) bool {
	if strings.ToLower(status.Account.Username) == "ripple" {
		log.Printf("%s is from myself\n", status.ID)
		return false
	}

	if status.InReplyToID != nil {
		log.Printf("%s is reply\n", status.ID)
		return false
	}

	if status.Reblog != nil {
		log.Printf("%s is reblog", status.ID)
		return false
	}

	if boolValue, ok := status.Bookmarked.(bool); ok && boolValue {
		log.Printf("already replied to %s\n", status.ID)
		return false
	}

	return true
}

func hasRelevantImage(status *mastodon.Status) (bool, string) {
	if len(status.MediaAttachments) == 0 {
		log.Println("post has no media attachments")
		return false, ""
	}

	for _, attachment := range status.MediaAttachments {
		if attachment.Type == "image" {
			return true, attachment.URL
		}
	}

	log.Println("post has no images")
	return false, ""
}

func (r *RippleBot) postReply() error {
	timeline, err := r.mastodonClient.GetTimelinePublic(context.TODO(), true, &mastodon.Pagination{
		Limit: 100,
	})
	if err != nil {
		return err
	}

	for _, status := range timeline {
		if !shouldRespondToPost(status) {
			log.Printf("shouldn't respond to %s\n", status.ID)
			continue
		}

		ok, imageUrl := hasRelevantImage(status)
		if !ok {
			log.Printf("%s has no relevant images\n", status.ID)
			continue
		}

		httpResponse, err := r.httpClient.Get(imageUrl)
		if err != nil {
			return err
		}

		image, err := io.ReadAll(httpResponse.Body)
		if err != nil {
			return err
		}

		encodedImage := base64.StdEncoding.EncodeToString(image)

		contentType := httpResponse.Header.Get("Content-Type")
		if contentType == "" {
			return fmt.Errorf("couldn't get content type for image")
		}

		prompt := strings.Join([]string{
			"Please generate a comment for the following image as Ripple.",
			"If the image contains people don't assume the poster is one of them.",
			fmt.Sprintf("The image was posted '%s' by '%s'.",
				status.CreatedAt.Format(time.RFC850),
				status.Account.DisplayName),
			fmt.Sprintf("The post contained the following text: '%s'", stripTags(status.Content)),
		}, " ")

		message, err := r.anthropicClient.Messages.New(context.TODO(), anthropic.MessageNewParams{
			MaxTokens: 2048,
			System: []anthropic.TextBlockParam{
				{
					Text: systemPrompt,
				},
			},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
				anthropic.NewUserMessage(anthropic.NewImageBlockBase64(contentType, encodedImage)),
			},
			Model: anthropic.ModelClaudeSonnet4_20250514,
		})
		if err != nil {
			return err
		}

		println(prompt)
		println(message.Content[0].Text)

		_, err = r.mastodonClient.PostStatus(context.TODO(), &mastodon.Toot{
			InReplyToID: status.ID,
			Status:      message.Content[0].Text,
			Visibility:  "public",
		})
		if err != nil {
			return err
		}
		log.Printf("responded to %s\n", status.ID)

		_, err = r.mastodonClient.Bookmark(context.TODO(), status.ID)
		if err != nil {
			return err
		}
		log.Printf("bookmarked %s\n", status.ID)

		break
	}

	return nil
}

func NewRippleBot(cmd *cli.Command) *RippleBot {
	httpTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient := &http.Client{Transport: httpTransport}

	mastodonConfig := &mastodon.Config{
		Server:       cmd.String("mastodon-server"),
		ClientID:     cmd.String("mastodon-client-id"),
		ClientSecret: cmd.String("mastodon-client-secret"),
		AccessToken:  cmd.String("mastodon-access-token"),
	}
	mastodonClient := mastodon.NewClient(mastodonConfig)
	mastodonClient.Transport = httpTransport

	anthropicOptions := []option.RequestOption{
		option.WithHTTPClient(httpClient),
		option.WithAPIKey(cmd.String("anthropic-api-key")),
	}

	cloudflareAiGateway := cmd.String("cloudflare-ai-gateway")
	if len(cloudflareAiGateway) > 0 {
		anthropicOptions = append(anthropicOptions, option.WithBaseURL(cloudflareAiGateway))
	}

	anthropicClient := anthropic.NewClient(anthropicOptions...)

	rippleBot := &RippleBot{
		httpClient,
		mastodonClient,
		&anthropicClient,
	}

	return rippleBot
}

func run(_ context.Context, cmd *cli.Command) error {
	rippleBot := NewRippleBot(cmd)

	return rippleBot.postReply()
}
