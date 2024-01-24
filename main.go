package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
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

//go:embed index.html
var home string

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

type File struct {
	URL            string
	size           int64
	name           string
	lastModifiedAt time.Time
}

func (f *File) HumanReadableSize() string {
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

func (f *File) ShortName() string {
	p := strings.Split(f.name, "/")
	return p[len(p)-1]
}

func (f *File) group() string {
	p := strings.Split(f.name, "/")
	if len(p) == 1 {
		return "Binários"
	}
	return p[0]
}

type Group struct {
	Name  string
	Files []File
}

func newGroups(fs []File) []Group {
	var m = make(map[string][]File)
	for _, f := range fs {
		n := f.group()
		m[n] = append(m[n], f)
	}
	ks := []string{}
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var gs []Group
	for _, k := range ks {
		gs = append(gs, Group{k, m[k]})
	}
	return gs
}

type Cache struct {
	settings  settings
	createdAt time.Time
	template  *template.Template
	HTML      []byte
}

func (c *Cache) isExpired() bool {
	return time.Since(c.createdAt) > cacheExpiration
}

func (c *Cache) refresh() error {
	var fs []File
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
	loadPage := func(t *string) ([]File, *string, error) {
		var fs []File
		sdk := s3.New(sess)
		r, err := sdk.ListObjectsV2(&s3.ListObjectsV2Input{
			Bucket:            aws.String(c.settings.bucket),
			ContinuationToken: t,
		})
		if err != nil {
			return []File{}, nil, err
		}
		for _, obj := range r.Contents {
			url := fmt.Sprintf("%s%s", c.settings.publicDomain, *obj.Key)
			fs = append(fs, File{url, *obj.Size, *obj.Key, *obj.LastModified})
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

	var b bytes.Buffer
	c.template.Execute(&b, newGroups(fs))
	c.HTML = b.Bytes()
	c.createdAt = time.Now()
	return nil
}

func newCache(s settings) (*Cache, error) {
	t, err := template.New("home").Parse(home)
	if err != nil {
		return nil, err
	}
	c := Cache{s, time.Now(), t, []byte{}}
	if err := c.refresh(); err != nil {
		return nil, err
	}
	return &c, nil
}

func startServer(c *Cache, p string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if c.isExpired() {
			if err := c.refresh(); err != nil {
				log.Output(1, fmt.Sprintf("Error loading files: %s", err))
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}
		w.Write(c.HTML)
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
