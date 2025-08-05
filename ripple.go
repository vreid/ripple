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

const botName = "ripple"

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
	if strings.ToLower(status.Account.Username) == botName {
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

	for _, mention := range status.Mentions {
		if mention.Username == botName {
			log.Printf("%s mentioned me <3\n", status.ID)
			return true
		}
	}

	return false
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

func (r *RippleBot) downloadImageToBase64(imageUrl string) (string, string, error) {
	httpResponse, err := r.httpClient.Get(imageUrl)
	if err != nil {
		return "", "", err
	}

	image, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return "", "", err
	}

	encodedImage := base64.StdEncoding.EncodeToString(image)

	contentType := httpResponse.Header.Get("Content-Type")
	if contentType == "" {
		return "", "", fmt.Errorf("couldn't get content type for image")
	}

	return encodedImage, contentType, nil
}

func (r *RippleBot) toMessageParam(status *mastodon.Status) (*anthropic.MessageParam, error) {
	images := []anthropic.ContentBlockParamUnion{}
	for _, attachment := range status.MediaAttachments {
		if attachment.Type == "image" {
			imageUrl := attachment.URL

			encodedImage, contentType, err := r.downloadImageToBase64(imageUrl)
			if err != nil {
				return nil, err
			}

			images = append(images, anthropic.NewImageBlockBase64(contentType, encodedImage))
		}
	}

	imageInfo := "no images"
	if len(images) > 0 {
		imageInfo = "the following images"
	}

	intro := fmt.Sprintf("This status was created '%s' by '%s', has the id '%s', had the following content and %s.",
		status.CreatedAt.Format(time.RFC850),
		status.Account.Username,
		status.ID,
		imageInfo,
	)

	blocks := []anthropic.ContentBlockParamUnion{}
	blocks = append(blocks, anthropic.NewTextBlock(intro))
	blocks = append(blocks, anthropic.NewTextBlock(stripTags(status.Content)))
	blocks = append(blocks, images...)

	messageParam := anthropic.NewUserMessage(blocks...)

	return &messageParam, nil
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

		encodedImage, contentType, err := r.downloadImageToBase64(imageUrl)
		if err != nil {
			return err
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

func (r *RippleBot) postQuote() error {
	prompt := strings.Join([]string{
		"Please generate a beer related quote of the day as Ripple.",
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
		},
		Model: anthropic.ModelClaudeSonnet4_20250514,
	})
	if err != nil {
		return err
	}

	println(prompt)
	println(message.Content[0].Text)

	_, err = r.mastodonClient.PostStatus(context.TODO(), &mastodon.Toot{
		Status:     message.Content[0].Text,
		Visibility: "public",
	})
	if err != nil {
		return err
	}

	return nil
}

func (r *RippleBot) checkNotifications() error {
	notifications, err := r.mastodonClient.GetNotifications(context.TODO(), &mastodon.Pagination{
		Limit: 100,
	})
	if err != nil {
		return err
	}

	for _, notification := range notifications {
		status := notification.Status
		if status == nil {
			log.Println("no status")
			continue
		}

		mentioned := false
		for _, mention := range status.Mentions {
			if mention.Username == botName {
				mentioned = true
				break
			}
		}

		if !mentioned {
			log.Printf("not mentioned in %s\n", status.ID)
			continue
		}

		if boolValue, ok := status.Bookmarked.(bool); ok && boolValue {
			log.Printf("already replied to %s\n", status.ID)
			continue
		}

		statusContext, err := r.mastodonClient.GetStatusContext(context.TODO(), status.ID)
		if err != nil {
			log.Printf("couldn't get context for %s: %s\n", status.ID, err.Error())
			continue
		}

		messageParams := []anthropic.MessageParam{}

		for _, ancestor := range statusContext.Ancestors {
			messageParam, err := r.toMessageParam(ancestor)
			if err != nil {
				log.Printf("couldn't convert %s to message param: %s\n", status.ID, err.Error())
				continue
			}

			messageParams = append(messageParams, *messageParam)
		}

		messageParam, err := r.toMessageParam(status)
		if err != nil {
			log.Printf("couldn't convert %s to message param: %s\n", status.ID, err.Error())
			continue
		} else {
			messageParams = append(messageParams, *messageParam)
		}

		for _, descendant := range statusContext.Descendants {
			fmt.Printf("A> %s %s\n", descendant.ID, descendant.Account.Username)

			messageParam, err := r.toMessageParam(descendant)
			if err != nil {
				log.Printf("couldn't convert %s to message param: %s\n", status.ID, err.Error())
				continue
			}

			messageParams = append(messageParams, *messageParam)
		}

		additionalSystemPrompt := strings.Join([]string{
			"Please generate a comment for the following Mastodon thread as Ripple.",
			"If the images contain people don't assume the users are in those pictures.",
			"It is possible that the thread already contains comments from you.",
			"Try to put a fitting hashtag at the end of the message.",
			"Feel free to respond in the language the thread is in.",
			fmt.Sprintf("You are replying to the post with the id '%s'.", status.ID),
		}, " ")
		message, err := r.anthropicClient.Messages.New(context.TODO(), anthropic.MessageNewParams{
			MaxTokens: 2048,
			System: []anthropic.TextBlockParam{
				{
					Text: systemPrompt,
				},
				{
					Text: additionalSystemPrompt,
				},
			},
			Messages: messageParams,
			Model:    anthropic.ModelClaudeSonnet4_20250514,
		})
		if err != nil {
			return err
		}

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

func RunReply(_ context.Context, cmd *cli.Command) error {
	rippleBot := NewRippleBot(cmd)

	return rippleBot.postReply()
}

func RunQuote(_ context.Context, cmd *cli.Command) error {
	rippleBot := NewRippleBot(cmd)

	return rippleBot.postQuote()
}

func RunCheckNotifications(_ context.Context, cmd *cli.Command) error {
	rippleBot := NewRippleBot(cmd)

	return rippleBot.checkNotifications()
}
