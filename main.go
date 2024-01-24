package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

const (
	cacheExpiration = 12 * time.Hour
	unit            = 1024
)

type settings struct {
	accessKey       string
	secretAccessKey string
	region          string
	endpointURL     string
	bucket          string
	publicDomain    string
}

func newSettings() (settings, error) {
	var m []string
	load := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			m = append(m, key)
		}
		return v
	}

	s := settings{
		accessKey:       load("ACCESS_KEY_ID"),
		secretAccessKey: load("SECRET_ACCESS_KEY"),
		region:          load("AWS_DEFAULT_REGION"),
		endpointURL:     load("ENDPOINT_URL"),
		bucket:          load("BUCKET"),
		publicDomain:    load("PUBLIC_DOMAIN"),
	}

	if len(m) > 0 {
		return settings{}, fmt.Errorf("missing environment variable(s): %s", strings.Join(m, ", "))
	}
	return s, nil

}

type file struct {
	url          string
	name         string
	size         int64
	lastModified time.Time
}

func (f *file) humanReadableSize() string {
	if f.size < unit {
		return fmt.Sprintf("%d B", f.size)
	}
	div, exp := int64(unit), 0
	for n := f.size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(f.size)/float64(div), "KMGTPE"[exp])
}

type cache struct {
	settings  settings
	createdAt time.Time
	data      []file
}

func (c *cache) isExpired() bool {
	return time.Since(c.createdAt) > cacheExpiration
}

func (c *cache) refresh() error {
	var fs []file
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(c.settings.region),
		Endpoint:         aws.String(c.settings.endpointURL),
		S3ForcePathStyle: aws.Bool(true),
		Credentials: credentials.NewStaticCredentials(
			c.settings.accessKey,
			c.settings.secretAccessKey,
			"",
		),
	})
	if err != nil {
		return err
	}

	var token *string
	loadPage := func(t *string) ([]file, *string, error) {
		var fs []file
		sdk := s3.New(sess)
		r, err := sdk.ListObjectsV2(&s3.ListObjectsV2Input{
			Bucket:            aws.String(c.settings.bucket),
			ContinuationToken: t,
		})
		if err != nil {
			return []file{}, nil, err
		}
		for _, obj := range r.Contents {
			url := fmt.Sprintf("%s%s", c.settings.publicDomain, *obj.Key)
			fs = append(fs, file{url, *obj.Key, *obj.Size, *obj.LastModified})
		}
		if *r.IsTruncated {
			return fs, r.NextContinuationToken, nil
		}
		return fs, nil, nil
	}
	for {
		r, nxt, err := loadPage(token)
		if err != nil {
			return err
		}
		fs = append(fs, r...)
		if nxt == nil {
			break
		}
		token = nxt
	}
	c.data = fs
	c.createdAt = time.Now()
	return nil
}

func newCache(s settings) (*cache, error) {
	c := cache{s, time.Now(), []file{}}
	if err := c.refresh(); err != nil {
		return &c, err
	}
	return &c, nil
}

func startServer(c *cache, p string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if c.isExpired() {
			if err := c.refresh(); err != nil {
				log.Output(1, fmt.Sprintf("Error loading files: %s", err))
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}

		fmt.Fprintf(w, "<h1>Minha Receita Mirror</h1>")
		fmt.Fprintf(w, "<ul>")
		for _, f := range c.data {
			fmt.Fprintf(
				w,
				"<li><a href=\"%s\">%s</a> (%s) %s</li>",
				f.url,
				f.name,
				f.humanReadableSize(),
				f.lastModified.Format("2006-01-02 15:04:05"),
			)
		}
		fmt.Fprintf(w, "</ul>")
	})

	p = fmt.Sprintf(":%s", p)
	log.Output(1, fmt.Sprintf("Server listening on http://0.0.0.0%s", p))
	http.ListenAndServe(p, nil)
}

func main() {
	s, err := newSettings()
	if err != nil {
		log.Fatal(err)
	}
	c, err := newCache(s)
	if err != nil {
		log.Fatal(err)
	}

	p := os.Getenv("PORT")
	if p == "" {
		p = "8000"
	}
	startServer(c, p)
}
