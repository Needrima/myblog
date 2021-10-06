package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/gomail.v2"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/crypto/bcrypt"
	//"go.mongodb.org/mongo-driver/mongo/readpref"
)

//global variables
var (
	tpl *template.Template

	ctx context.Context

	blogPosts    *mongo.Collection
	blogComments *mongo.Collection
	emails       *mongo.Collection

	fm = template.FuncMap{
		"rbc": ReduceBlogContent,
		"inc": Inc,
		"dec": Dec,
	}
)

//blog structs
type Comment struct {
	DatabaseID primitive.ObjectID `bson:"_id"`
	BelongsTo  string             `bson:"belongsto"` // ID of owning blog post
	Commentor  string             `bson:"commentor"`
	Comment    string             `bson:"comment"`
}

type NewPost struct {
	DatabaseID   primitive.ObjectID `bson:"_id"`
	ID           string             `bson:"id"`
	Title        string             `bson:"title"`
	Published    time.Time          `bson:"published"`
	ReadTime     float64            `bson:"readtime"`
	Content      string             `bson:"content"`
	ImageName    string             `bson:"imagename"`
	BpTitle      string             `bson:"bptitle"` //bullet point title
	BulletPoints []string           `bson:"bulletpoint"`
	BqTitle      string             `bson:"bqtitle"` //blog quote title
	BlogQuote    string             `bson:"blogquote"`
	QuoteAuthor  string             `bson:"quoteauthor"`
	VideoPath    string             `bson:"videopath"` // Youtube video path
	Comments     []Comment          `bson:"comments"`
}

type BlogPost struct {
	NewPost
	NumComment    int
	PublishedDate string
}

type BlogPostAndPageNumber struct {
	BlogPosts  []BlogPost
	PageNumber int
}

type Subscriber struct {
	DatabaseID primitive.ObjectID `bson:"_id"`
	Mail       string             `bson:"mail"`
}

func main() {
	// database connection
	// uri := os.Getenv("atlasURI")
	clientOptions := options.Client().ApplyURI("mongodb://localhost:27017")

	ctx = context.Background()

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal("client" + err.Error())
		return
	}
	defer client.Disconnect(ctx)

	database := client.Database("student-devs-blog")

	blogPosts = database.Collection("blog-posts")

	blogComments = database.Collection("blog-comments")

	emails = database.Collection("emails")

	//routing and serving
	routes()

	port := os.Getenv("PORT")

	if port == "" {
		port = "8080"
	}

	http.ListenAndServe(":"+port, nil)
}

// http handler functions

//redirects to home
func Visit(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/home", 303)
}

func Home(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", 405)
		return
	}

	//get first eight posts from database
	limit, skip := int64(8), int64(0)
	findOptions := options.FindOptions{
		Limit: &limit,
		Skip:  &skip,
		Sort:  bson.M{"published": -1},
	}

	cursor, err := blogPosts.Find(ctx, bson.M{}, &findOptions)
	if err != nil {
		http.Error(w, "Find: "+err.Error(), 500)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	data := BlogPostAndPageNumber{BlogPosts: blogPosts}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost { // user trying to subbscibe
		if err := regiterSubscriber(r); err != nil {

			if err.Error() == "Unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", 400)
				return
			} else if err.Error() == "You are already a subscriber" { // user already a subsciber
				http.Error(w, err.Error(), 400)
				return
			}

			// any other error
			log.Println("Error sub:", err)
			http.Error(w, "Something went wrong, try again later", 500)
			return
		}

		tpl.ExecuteTemplate(w, "index.html", data)
	}
}

// calls next eight blogposts
func Next(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", 405)
		return
	}

	pageNumber, _ := strconv.Atoi(r.URL.Path[len("/next/"):])
	pageNumber++

	// gets next eight blogposts
	limit, skip := int64(8), int64(8*pageNumber)
	findOptions := options.FindOptions{
		Limit: &limit,
		Skip:  &skip,
		Sort:  bson.M{"published": -1},
	}

	cursor, err := blogPosts.Find(ctx, bson.M{}, &findOptions)
	if err != nil {
		http.Error(w, "Find: "+err.Error(), 500)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	// if there are no more blogPosts in database
	if len(blogPosts) == 0 {
		tpl.ExecuteTemplate(w, "page-end.html", nil)
		return
	}

	data := BlogPostAndPageNumber{blogPosts, pageNumber}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "Unregistered" { // unregistered/unreachable email address
				http.Error(w, "Unregistered email address or email is undeliverable", 400)
				return
			} else if err.Error() == "You are already a subscriber" { // user already a subsciber
				http.Error(w, err.Error(), 400)
				return
			}

			// any other error
			log.Println("Error sub:", err)
			http.Error(w, "Something went wrong, try again later", 500)
			return
		}

		tpl.ExecuteTemplate(w, "index.html", data)
	}
}

// gets previous eight posts
func Previous(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", 405)
		return
	}

	pageNumber, _ := strconv.Atoi(r.URL.Path[len("/previous/"):])
	pageNumber--

	if pageNumber < 1 {
		http.Redirect(w, r, "/home", 303)
		return
	}

	limit, skip := int64(8), int64(8*pageNumber)
	findOptions := options.FindOptions{
		Limit: &limit,
		Skip:  &skip,
		Sort:  bson.M{"published": -1},
	}

	cursor, err := blogPosts.Find(ctx, bson.M{}, &findOptions)
	if err != nil {
		http.Error(w, "Find: "+err.Error(), 500)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	data := BlogPostAndPageNumber{blogPosts, pageNumber}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "Unregistered" { // unregistered/unreachable email address
				http.Error(w, "Unregistered email address", 400)
				return
			} else if err.Error() == "You are already a subscriber" { // user already a subsciber
				http.Error(w, err.Error(), 400)
				return
			}

			// any other error
			log.Println("Error sub:", err)
			http.Error(w, "Something went wrong, try again later", 500)
			return
		}

		tpl.ExecuteTemplate(w, "index.html", data)
	}
}

func Blog(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	id := path[6:]
	//fmt.Println(id)

	if r.Method == http.MethodGet { // render blogPosts
		post, err := getSinglePostFromID(id)
		if err != nil {
			http.Error(w, "Something went wrong: "+err.Error(), 500)
			return
		}

		tpl.ExecuteTemplate(w, "blog-post.html", post)
	} else if r.Method == http.MethodPost { // user trying to comment
		//get comment
		comment, err := getNewComment(r, id)
		if err != nil {
			if err.Error() == "You already made this reply" {
				log.Println("You already made this reply")
				http.Error(w, err.Error(), 400)
				return
			}

			http.Error(w, err.Error(), 500)
			return
		}

		// store in database
		result, err := blogComments.InsertOne(ctx, comment)
		if err != nil {
			http.Error(w, "Something went wrong", 500)
			return
		}
		fmt.Println(result.InsertedID)

		http.Redirect(w, r, path, 303)
	}
}

func About(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", 405)
		return
	}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "about.html", nil)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "Unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", 400)
				return
			} else if err.Error() == "You are already a subscriber" { // user already a subsciber
				http.Error(w, err.Error(), 400)
				return
			}

			// any other error
			log.Println("Error sub:", err)
			http.Error(w, "Something went wrong, try again later", 500)
			return
		}

		http.Redirect(w, r, "/about", 303)
	}
}

func NewBlog(w http.ResponseWriter, r *http.Request) {
	//check for get and post
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", 405)
		return
	}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "new-post.html", nil)
	} else if r.Method == http.MethodPost {
		//get for data
		post, err := getNewPost(r)
		if err != nil {
			http.Error(w, "Error: "+err.Error(), 500)
			return
		}

		//store in database
		_, err = blogPosts.InsertOne(ctx, post)
		if err != nil {
			http.Error(w, "Inserting post: "+err.Error(), 500)
			return
		}

		tpl.ExecuteTemplate(w, "new-post.html", "Post added")
	}

}

// serve Favicon
func ServeFavicon(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "templates/favicon.ico")
}

//helping functions

// initializes templates parsing
func init() {
	tpl = template.Must(template.New("").Funcs(fm).ParseGlob("templates/*.html"))
}

func routes() {
	//handlers
	http.HandleFunc("/", Visit)
	http.HandleFunc("/home", Home)
	http.HandleFunc("/next/", Next)
	http.HandleFunc("/previous/", Previous)
	http.HandleFunc("/blog/", Blog)
	http.HandleFunc("/about", About)
	http.HandleFunc("/admin/new", NewBlog)
	http.HandleFunc("/favicon.ico/", ServeFavicon)

	//folders
	//css
	http.Handle("/assets/css/", http.StripPrefix("/assets/css/", http.FileServer(http.Dir("assets/css"))))
	//images
	http.Handle("/assets/images/", http.StripPrefix("/assets/images/", http.FileServer(http.Dir("assets/images"))))
	http.Handle("/assets/images/blog/", http.StripPrefix("/assets/images/blog/", http.FileServer(http.Dir("assets/images/blog"))))
	//js
	http.Handle("/assets/js/", http.StripPrefix("/assets/js/", http.FileServer(http.Dir("assets/js"))))
	http.Handle("/assets/js/demo/", http.StripPrefix("/assets/js/demo/", http.FileServer(http.Dir("assets/js/demo"))))
	//plugins
	http.Handle("/assets/plugins/", http.StripPrefix("/assets/plugins/", http.FileServer(http.Dir("assets/plugins"))))
	http.Handle("/assets/plugins/bootstrap/js/", http.StripPrefix("/assets/plugins/bootstrap/js/", http.FileServer(http.Dir("assets/plugins/bootstrap/js"))))
	//scss
	http.Handle("/assets/scss/", http.StripPrefix("/assets/scss/", http.FileServer(http.Dir("assets/scss"))))
	//scss bootstrap js subfolders
	http.Handle("/assets/scss/bootstrap/js/dist/", http.StripPrefix("/assets/scss/bootstrap/js/dist/", http.FileServer(http.Dir("assets/scss/bootstrap/js/dist"))))
	http.Handle("/assets/scss/bootstrap/js/src/", http.StripPrefix("/assets/scss/bootstrap/js/src/", http.FileServer(http.Dir("assets/scss/bootstrap/js/src"))))
	http.Handle("/assets/scss/bootstrap/js/tests/", http.StripPrefix("/assets/scss/bootstrap/js/tests/", http.FileServer(http.Dir("assets/scss/bootstrap/js/tests"))))
	http.Handle("/assets/scss/bootstrap/js/tests/integration/", http.StripPrefix("/assets/scss/bootstrap/js/tests/integration/", http.FileServer(http.Dir("assets/scss/bootstrap/js/tests/integration"))))
	http.Handle("/assets/scss/bootstrap/js/tests/unit/", http.StripPrefix("/assets/scss/bootstrap/js/tests/unit/", http.FileServer(http.Dir("assets/scss/bootstrap/js/tests/unit"))))
	http.Handle("/assets/scss/bootstrap/js/tests/visual/", http.StripPrefix("/assets/scss/bootstrap/js/tests/visual/", http.FileServer(http.Dir("assets/scss/bootstrap/js/tests/visual"))))
	//scss bootstrap scss
	http.Handle("/assets/scss/bootstrap/scss/", http.StripPrefix("/assets/scss/bootstrap/scss/", http.FileServer(http.Dir("assets/scss/bootstrap/scss"))))
	//scss bootstrap scss subfolderss
	http.Handle("/assets/scss/bootstrap/scss/mixins/", http.StripPrefix("/assets/scss/bootstrap/scss/mixins/", http.FileServer(http.Dir("assets/scss/bootstrap/scss/mixins"))))
	http.Handle("/assets/scss/bootstrap/scss/utilities/", http.StripPrefix("/assets/scss/bootstrap/scss/utilities/", http.FileServer(http.Dir("assets/scss/bootstrap/scss/utilities"))))
	http.Handle("/assets/scss/bootstrap/scss/vendor/", http.StripPrefix("/assets/scss/bootstrap/scss/vendor/", http.FileServer(http.Dir("assets/scss/bootstrap/scss/vendor"))))
	//scss theme
	http.Handle("/assets/scss/theme/", http.StripPrefix("/assets/scss/theme/", http.FileServer(http.Dir("assets/scss/theme"))))
}

//checks if method is get or post
func ValidMethod(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return false
	}
	return true
}

//validate form inputs
func valid(input, exp string) bool {
	return regexp.MustCompile(exp).MatchString(input)
}

//processe form and gets new post
func getNewPost(r *http.Request) (post NewPost, err error) {
	// database information
	database_ID := primitive.NewObjectID()

	ID := database_ID.String()[10:34]

	pub_Time := time.Now()

	comments := []Comment{}

	// get and validate form input
	title, exp := r.FormValue("title"), `.*`
	if !valid(title, exp) {
		return NewPost{}, errors.New("Invalid character in blog title")
	}

	content, exp := r.FormValue("content"), `.*`
	if !valid(content, exp) {
		return NewPost{}, errors.New("Invalid character in content")
	}

	bp_heading, exp := r.FormValue("bullet-point-Heading"), `^[\sa-zA-Z0-9\.,\?/\\]+$`
	if !valid(bp_heading, exp) {
		return NewPost{}, errors.New("Invalid character in bullet point heading")
	}

	bullet_point_content, exp := r.FormValue("bullet-points-content"), `.*`
	if !valid(bullet_point_content, exp) {
		return NewPost{}, errors.New("Invalid character in bullet points content")
	}
	bullet_points := strings.Split(bullet_point_content, "/")

	bq_heading, exp := r.FormValue("blog-quote-Heading"), `^[\sa-zA-Z0-9\.,\?/\\]+$`
	if !valid(bq_heading, exp) {
		return NewPost{}, errors.New("Invalid character in blog quote heading")
	}

	blog_quote, exp := r.FormValue("blog-quote"), `.*`
	if !valid(blog_quote, exp) {
		return NewPost{}, errors.New("Invalid character in blog quote content")
	}

	quote_author, exp := r.FormValue("quote-author"), `^[\sa-zA-Z]+$`
	if !valid(quote_author, exp) {
		return NewPost{}, errors.New("Invalid character in blog quote author")
	}

	video_path, exp := r.FormValue("youtube-VideoPath"), `^[\sa-zA-Z0-9_]+$`
	if !valid(video_path, exp) {
		return NewPost{}, errors.New("Invalid character in youtube video path")
	}

	admin_password, exp := r.FormValue("adminPassword"), `.*`
	if !valid(admin_password, exp) {
		return NewPost{}, errors.New("Invalid character in admin password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(os.Getenv("adminPassword")), []byte(admin_password)); err != nil {
		return NewPost{}, errors.New("Invalid admin password: " + err.Error())
	}

	// process image file
	file, img_header, err := r.FormFile("blogImage")
	if err != nil {
		return NewPost{}, err
	}
	ext := filepath.Ext(img_header.Filename)

	img_Name, err := uploadImageAndReturnName(file, ext, ID)
	if err != nil {
		return NewPost{}, err
	}

	read_Time := math.Round(float64(len(title)+len(content)+len(bp_heading)+len(bullet_point_content)+len(blog_quote)+len(quote_author)) / 100)

	// get subscribers email address
	subscribers, err := getAllSubscribers()

	if err != nil { // delete image file and return error message
		files, _ := ioutil.ReadDir("./assets/images/blog")
		for _, v := range files {
			if strings.Contains(v.Name(), ID) {
				os.Remove("./assets/images/blog/" + ID)
				break
			}
		}

		return NewPost{}, err
	}

	// send mail to subscibers
	if err := sendMailOnNewBlogPost(subscribers, ID, title); err != nil {
		return NewPost{}, err
	}

	// return new post data
	return NewPost{database_ID, ID, title, pub_Time, read_Time, content, img_Name, bp_heading, bullet_points, bq_heading, blog_quote, quote_author, video_path, comments}, nil
}

//checks if a string exists in a slice of strings
func Found(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}

//processes image, store in images folder and retrieve its name
func uploadImageAndReturnName(file multipart.File, ext, ID string) (name string, err error) {
	//check for correct file type
	allowedExt := []string{".jpeg", ".jpg", ".png"}
	if !Found(allowedExt, ext) {
		return "", errors.New("Unaccepted image, only .jpeg, .png, .jpg")
	}

	//store image in blog directory
	bs, err := ioutil.ReadAll(file)
	if err != nil {
		return "", errors.New("Reaing image file error: " + err.Error())
	}

	f, err := ioutil.TempFile("assets/images/blog", ID+"-*"+ext)
	if err != nil {
		return "", errors.New("Tempfile: " + err.Error())
	}
	defer f.Close()
	f.Write(bs)

	// get image name from blog directory
	files, err := ioutil.ReadDir("assets/images/blog")
	if err != nil {
		return "", errors.New("Reading images directory: " + err.Error())
	}

	var img_Name string
	for _, v := range files {
		if strings.Contains(v.Name(), ID) {
			img_Name = v.Name()
		}
	}

	return img_Name, nil
}

// checks if user is already subscribed
func alreadySubcribed(email string) bool {
	singleResult := emails.FindOne(ctx, bson.M{"mail": email})
	if singleResult.Err() != nil {
		return false
	}
	return true
}

// register subscriber
func regiterSubscriber(r *http.Request) error {
	//valide email
	Email, exp := template.HTMLEscaper(r.FormValue("semail1")), `^([a-zA-z0-9.!#$%&'*+/=?^_{|}~-]{3,})@([a-zA-Z0-9]{2,})\.([a-zA-Z]{2,})(.[a-zA-Z]+)?$`
	if !valid(Email, exp) {
		return errors.New("invalid email address")
	}

	// check if email is registered / reachable using "mailboxlayer api"
	if err := checkIfEmailIsRegistered(Email); err != nil {
		return err
	}

	//check if user is already subscribed
	if alreadySubcribed(Email) {
		return errors.New("You are already a subscriber")
	}

	// send welcome message
	if err := sendWelcomeMail(Email); err != nil {
		return err
	}

	// register new subscriber
	newSubscriber := Subscriber{primitive.NewObjectID(), Email}

	if _, err := emails.InsertOne(ctx, newSubscriber); err != nil {
		return errors.New("An error occured")
	}

	//fmt.Println(result.InsertedID)
	return nil
}

// get post comment from post id
func getPostComments(ID string) []Comment {
	var comments []Comment

	cursor, err := blogComments.Find(ctx, bson.M{"belongsto": ID})
	if err != nil {
		log.Println("Finding comments: " + err.Error())
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var comment Comment

		if err := cursor.Decode(&comment); err != nil {
			log.Println("Comment decode:", err)
		}

		comments = append(comments, comment)
	}
	return comments
}

// get blogpost from cursor
func getBlogPostsFromCursor(cursor *mongo.Cursor) []BlogPost {
	var blogPosts []BlogPost
	for cursor.Next(ctx) {
		var post NewPost

		if err := cursor.Decode(&post); err != nil {
			log.Println("Cursor decode", err)
		}

		post.Comments = getPostComments(post.ID)

		blogPost := BlogPost{post, len(post.Comments), post.Published.Format(time.ANSIC)}

		blogPosts = append(blogPosts, blogPost)
	}

	return blogPosts
}

// get a single post from post id
func getSinglePostFromID(ID string) (BlogPost, error) {
	var singlePost NewPost

	singleResult := blogPosts.FindOne(ctx, bson.M{"id": ID})

	if err := singleResult.Decode(&singlePost); err != nil {
		return BlogPost{}, err
	}

	singlePost.Comments = getPostComments(ID)

	return BlogPost{singlePost, len(singlePost.Comments), singlePost.Published.Format(time.ANSIC)}, nil
}

func getNewComment(r *http.Request, id string) (Comment, error) {
	// validate form
	commentor, exp := template.HTMLEscaper(r.FormValue("commentor")), `^[a-zA-Z\s_]{2,35}$`
	if !valid(commentor, exp) {
		return Comment{}, errors.New(`Invalid input in name field or name not given, only "_" special character is allowed in name field a minimum of two characters and maximum of 35 characters`)
	}

	comment, exp := template.HTMLEscaper(r.FormValue("comment")), `^[\sa-zA-Z0-9\.,\?/\\~!@#\\$%\[}\]{\^\&\*()-_\+=\|:;'"<>]+$`
	if !valid(comment, exp) {
		return Comment{}, errors.New("Invalid input in comment field")
	}

	cursor, err := blogComments.Find(ctx, bson.M{})
	if err != nil {
		return Comment{}, errors.New("Something went wrong")
	}

	for cursor.Next(ctx) {
		var c Comment

		if err := cursor.Decode(&c); err != nil {
			return Comment{}, errors.New("Something went wrong")
		}

		if strings.ToLower(strings.TrimSpace(c.Commentor)) == strings.ToLower(strings.TrimSpace(commentor)) && strings.ToLower(strings.TrimSpace(c.Comment)) == strings.ToLower(strings.TrimSpace(comment)) {
			return Comment{}, errors.New("You already made this reply")
		}
	}

	database_ID := primitive.NewObjectID()
	belongsto := id
	return Comment{database_ID, belongsto, commentor, comment}, nil
}

// reduce blog content for home page
func ReduceBlogContent(content string) string {
	if len(content) > 219 {
		return content[:219]
	}

	return content
}

// increment page number for routing
func Inc(x int) int {
	x++
	return x
}

// decrement page number for routing
func Dec(x int) int {
	x--
	return x
}

// using debounce API to validate email registration
func checkIfEmailIsRegistered(email string) error {
	//{"debounce":{"email":"oyebodeamirdeen@gmail.com","code":"5","role":"false","free_email":"true","result":"Safe to Send","reason":"Deliverable","send_transactional":"1","did_you_mean":""},"success":"1","balance":"88"}
	type Debounce struct {
		Result string `json:"result"`
		Reason string `json:"reason"`
	}

	type DeliverableEmail struct {
		Debounce `json:"debounce"`
	}

	access_key := os.Getenv("emailValidator_access_key")
	fmt.Println(email)
	resp, err := http.Get(fmt.Sprintf("https://api.debounce.io/v1/?api=%s&email=%s", access_key, email))
	if err != nil {
		log.Fatal("Resp:", err)
	}
	defer resp.Body.Close()

	bs, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal("ReadAll:", err)
	}

	fmt.Println(string(bs))

	m := DeliverableEmail{}

	err = json.Unmarshal(bs, &m)
	if err != nil {
		log.Fatal("Unmarshal:", err)
	}

	fmt.Println(m.Result, m.Reason)

	// smtp_check is usually false for unregistered/ unreachable emails and score is usually less than 0.5
	if m.Result != "Safe to Send" || m.Reason != "Deliverable" {
		return errors.New("Unregistered")
	}

	return nil
}

// send mail with gmail IMAP
func sendWelcomeMail(email string) error {
	mail := gomail.NewMessage()

	mail.SetHeader("From", mail.FormatAddress("oyebodeamirdeen@outlook.com", "Needrima"))

	mail.SetHeaders(map[string][]string{
		"To":      {email},
		"Subject": {"Welcome to Needrima's Blog"},
	})

	password := os.Getenv("emailPassword")

	mail.SetBody("text/html", `Welcome to Needrima's blog. I'm Needrima and I'm pleased to have you on board. <a style="color:red;" href="http://needrimasblog.herokuapp.com">Visit</a> now to start reading my latest posts.`)

	dialer := gomail.NewDialer("smtp.gmail.com", 587, "oyebodeamirdeen@gmail.com", password)

	dialer.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	if err := dialer.DialAndSend(mail); err != nil {
		fmt.Println("Error sending mail:", err)
		return errors.New("sending welcome message failed")
	}

	return nil
}

// gets subscribers emails from database and return their mails
func getAllSubscribers() ([]string, error) {
	cursor, err := emails.Find(ctx, bson.M{})
	if err != nil {
		return []string{}, errors.New("Querying database failed")
	}

	var emails []string

	for cursor.Next(ctx) {
		var sub Subscriber

		if err := cursor.Decode(&sub); err != nil {
			log.Println("Error getting subsciber email")
			continue
		}

		emails = append(emails, sub.Mail)
	}

	fmt.Println(emails)

	return emails, nil
}

// send mail on new blogpost to all subscribers
func sendMailOnNewBlogPost(emails []string, blogId, title string) error {
	mail := gomail.NewMessage()

	mail.SetHeader("From", mail.FormatAddress("oyebodeamirdeen@outlook.com", "Needrima"))

	mail.SetHeaders(map[string][]string{
		"To":      emails,
		"Subject": {title + " at Needrima's blog"},
	})

	password := os.Getenv("emailPassword")

	body := fmt.Sprintf(`I just posted a new blog titled <b>%s</b> check it out <a style="color:red;" href="http://needrimasblog.herokuapp.com/blog/%s">Here</a>.`, title, blogId)

	mail.SetBody("text/html", body)

	dialer := gomail.NewDialer("smtp.gmail.com", 587, "oyebodeamirdeen@gmail.com", password)

	dialer.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	if err := dialer.DialAndSend(mail); err != nil {
		fmt.Println("Error sending mail:", err)
		return errors.New("sending new blog message failed")
	}

	return nil
}
