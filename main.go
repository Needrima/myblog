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

	gomail "gopkg.in/gomail.v2"

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
	blogReplies  *mongo.Collection
	emails       *mongo.Collection

	fm = template.FuncMap{
		"rbc": ReduceBlogContent,
		"inc": Inc,
		"dec": Dec,
	}
)

//blog structs

type Reply struct {
	DatabaseID primitive.ObjectID `bson:"_id"`
	BelongsTo  string             `bson:"belongsto"` // ID of owning comment
	Replier    string             `bson:"commentor"`
	Reply      string             `bson:"comment"`
}

type Comment struct {
	DatabaseID primitive.ObjectID `bson:"_id"`
	ID         string             `bson:"id"`
	BelongsTo  string             `bson:"belongsto"` // ID of owning blog post
	Commentor  string             `bson:"commentor"`
	Comment    string             `bson:"comment"`
	Replies    []Reply            `bson:"replies"`
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
	//uri := os.Getenv("atlasURI")
	shellURI := "mongodb://localhost:27017"
	clientOptions := options.Client().ApplyURI(shellURI)

	ctx = context.Background()

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatal("client" + err.Error())
	}
	defer client.Disconnect(ctx)

	database := client.Database("student-devs-blog")

	blogPosts = database.Collection("blog-posts")

	blogComments = database.Collection("blog-comments")

	blogReplies = database.Collection("blog-replies")

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
	http.Redirect(w, r, "/home", http.StatusSeeOther)
}

func Home(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		http.Error(w, "Find: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	data := BlogPostAndPageNumber{BlogPosts: blogPosts}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost { // user trying to subbscibe
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		dataAndSubscriptionSucess := struct {
			BlogPostAndPageNumber
			SubscriptionSucess string
		}{data, "Subscription sucessful"}

		tpl.ExecuteTemplate(w, "index.html", dataAndSubscriptionSucess)
	}
}

// calls next eight blogposts
func Next(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pageNumber, _ := strconv.Atoi(r.URL.Path[len("/next/"):])
	//pageNumber++

	// gets next eight blogposts
	limit, skip := int64(8), int64(8*pageNumber)
	findOptions := options.FindOptions{
		Limit: &limit,
		Skip:  &skip,
		Sort:  bson.M{"published": -1},
	}

	cursor, err := blogPosts.Find(ctx, bson.M{}, &findOptions)
	if err != nil {
		http.Error(w, "Find: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	// if there are no more blogPosts in database
	if len(blogPosts) == 0 {
		//tpl.ExecuteTemplate(w, "page-end.html", nil)
		pageNumber--
		http.Redirect(w, r, "/previous/"+strconv.Itoa(pageNumber), http.StatusSeeOther)
		return
	}

	data := BlogPostAndPageNumber{blogPosts, pageNumber}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		tpl.ExecuteTemplate(w, "index.html", data)
	}
}

// gets previous eight posts
func Previous(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pageNumber, _ := strconv.Atoi(r.URL.Path[len("/previous/"):])
	//pageNumber--

	if pageNumber < 1 {
		http.Redirect(w, r, "/home", http.StatusSeeOther)
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
		http.Error(w, "Find: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	blogPosts := getBlogPostsFromCursor(cursor)

	data := BlogPostAndPageNumber{blogPosts, pageNumber}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "index.html", data)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
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
			http.Error(w, "Something went wrong: "+err.Error(), http.StatusInternalServerError)
			return
		}

		tpl.ExecuteTemplate(w, "blog-post.html", post)
	} else if r.Method == http.MethodPost { // user trying to comment
		//get comment
		comment, err := getNewComment(r, id)
		if err != nil {
			if err.Error() == "You already made this reply" {
				log.Println("You already made this reply")
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// store in database
		_, err = blogComments.InsertOne(ctx, comment)
		if err != nil {
			http.Error(w, "Something went wrong", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, path, http.StatusSeeOther)
	}
}

// handle replies to a comment
func ReplyToComment(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	commentToReplyID := path[len("/reply/"):]

	var comment Comment

	if err := blogComments.FindOne(ctx, bson.M{"id": commentToReplyID}).Decode(&comment); err != nil {
		log.Println("Reply error", err)
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "reply.html", comment)
	} else if r.Method == http.MethodPost {
		reply, err := getNewReply(r, commentToReplyID)
		if err != nil {
			if err.Error() == "You already made this reply" {
				log.Println("You already made this reply")
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_, err = blogReplies.InsertOne(ctx, reply)
		if err != nil {
			http.Error(w, "Something went wrong", http.StatusInternalServerError)
			return
		}

		var owningPost NewPost
		if err := blogPosts.FindOne(ctx, bson.M{"id": comment.BelongsTo}).Decode(&owningPost); err != nil {
			log.Println("Getting owning blogpost error", err)
			http.Error(w, "Something went wrong", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/blog/"+owningPost.ID, http.StatusSeeOther)
	}
}

func About(w http.ResponseWriter, r *http.Request) {
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "about.html", nil)
	} else if r.Method == http.MethodPost {
		if err := regiterSubscriber(r); err != nil {
			if err.Error() == "unregistered" { // unregistered/unreachable email address
				http.Error(w, "Email not deliverable. Check that email is correct or try again later", http.StatusBadRequest)
				return
			}

			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/about", http.StatusSeeOther)
	}
}

func NewBlog(w http.ResponseWriter, r *http.Request) {
	//check for get and post
	if !ValidMethod(r) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodGet {
		tpl.ExecuteTemplate(w, "new-post.html", nil)
	} else if r.Method == http.MethodPost {
		//get for data
		post, err := getNewPost(r)
		if err != nil {
			http.Error(w, "Error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		//store in database
		_, err = blogPosts.InsertOne(ctx, post)
		if err != nil {
			http.Error(w, "Inserting post: "+err.Error(), http.StatusInternalServerError)
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
	http.HandleFunc("/reply/", ReplyToComment)
	http.HandleFunc("/about", About)
	http.HandleFunc("/admin/new", NewBlog)
	http.HandleFunc("/favicon.ico/", ServeFavicon)

	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("./assets"))))
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
		return NewPost{}, errors.New("invalid character in blog title")
	}

	content, exp := r.FormValue("content"), `.*`
	if !valid(content, exp) {
		log.Println()
		return NewPost{}, errors.New("invalid character in content")
	}

	bp_heading, exp := r.FormValue("bullet-point-Heading"), `^[\sa-zA-Z0-9\.,\?/\\]+$`
	if !valid(bp_heading, exp) {
		return NewPost{}, errors.New("invalid character in bullet point heading")
	}

	bullet_point_content, exp := r.FormValue("bullet-points-content"), `.*`
	if !valid(bullet_point_content, exp) {
		return NewPost{}, errors.New("invalid character in bullet points content")
	}
	bullet_points := strings.Split(bullet_point_content, "/")

	bq_heading, exp := r.FormValue("blog-quote-Heading"), `^[\sa-zA-Z0-9\.,\?/\\]+$`
	if !valid(bq_heading, exp) {
		return NewPost{}, errors.New("invalid character in blog quote heading")
	}

	blog_quote, exp := r.FormValue("blog-quote"), `.*`
	if !valid(blog_quote, exp) {
		return NewPost{}, errors.New("invalid character in blog quote content")
	}

	quote_author, exp := r.FormValue("quote-author"), `.*`
	if !valid(quote_author, exp) {
		return NewPost{}, errors.New("invalid character in blog quote author")
	}

	video_path, exp := r.FormValue("youtube-VideoPath"), `^[\sa-zA-Z0-9_]{0,}$`
	if !valid(video_path, exp) {
		return NewPost{}, errors.New("invalid character in youtube video path")
	}

	admin_password, exp := r.FormValue("adminPassword"), `.*`
	if !valid(admin_password, exp) {
		return NewPost{}, errors.New("invalid character in admin password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(os.Getenv("adminPassword")), []byte(admin_password)); err != nil {
		return NewPost{}, errors.New("invalid admin password: " + err.Error())
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
		return "", errors.New("unaccepted image, only .jpeg, .png, .jpg")
	}

	//store image in blog directory
	bs, err := ioutil.ReadAll(file)
	if err != nil {
		return "", errors.New("reading image file error: " + err.Error())
	}

	f, err := ioutil.TempFile("assets/images/blog", ID+"-*"+ext)
	if err != nil {
		return "", errors.New("tempfile: " + err.Error())
	}
	defer f.Close()
	f.Write(bs)

	// get image name from blog directory
	files, err := ioutil.ReadDir("assets/images/blog")
	if err != nil {
		return "", errors.New("reading images directory: " + err.Error())
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
	fmt.Println("Email from subscriber:", Email)
	if !valid(Email, exp) {
		return errors.New("invalid email address")
	}

	// check if email is registered / reachable using "mailboxlayer api"
	if err := checkIfEmailIsRegistered(Email); err != nil {
		return err
	}

	//check if user is already subscribed
	if alreadySubcribed(Email) {
		return errors.New("you are already a subscriber")
	}

	// send welcome message
	if err := sendWelcomeMail(Email); err != nil {
		return err
	}

	// register new subscriber
	newSubscriber := Subscriber{primitive.NewObjectID(), Email}

	if _, err := emails.InsertOne(ctx, newSubscriber); err != nil {
		log.Println("Error storing email to database")
		return errors.New("an error occured")
	}

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

	var newComments []Comment

	for _, comment := range comments {
		comment.Replies = getCommentReplies(comment.ID)
		newComments = append(newComments, comment)
	}

	return newComments
}

func getCommentReplies(commentID string) []Reply {
	// fmt.Println("comment id", commentID)
	// fmt.Println("------------------------------------------------------------------")

	var replies []Reply
	cur, err := blogReplies.Find(ctx, bson.M{"belongsto": commentID})
	if err != nil {
		log.Println("Error getting reply cursor", err)
	}
	defer cur.Close(ctx)

	if err := cur.All(ctx, &replies); err != nil {
		log.Println("Error getting reply", err)
	}

	return replies
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

	// fmt.Println("Singlepost before comment", singlePost)
	// fmt.Println("------------------------------------------------------------------")

	singlePost.Comments = getPostComments(ID)

	// fmt.Println("Singlepost after comment", singlePost)
	// fmt.Println("------------------------------------------------------------------")

	return BlogPost{singlePost, len(singlePost.Comments), singlePost.Published.Format(time.ANSIC)}, nil
}

func getNewComment(r *http.Request, id string) (Comment, error) {
	// validate form
	commentor, exp := template.HTMLEscaper(r.FormValue("commentor")), `^[a-zA-Z\s_]{2,35}$`
	if !valid(commentor, exp) {
		return Comment{}, errors.New(`invalid input in name field or name not given, only "_" special character is allowed in name field a minimum of two characters and maximum of 35 characters`)
	}

	comment, exp := template.HTMLEscaper(r.FormValue("comment")), `.*`
	if !valid(comment, exp) {
		return Comment{}, errors.New("invalid input in comment field")
	}

	cursor, err := blogComments.Find(ctx, bson.M{})
	if err != nil {
		return Comment{}, errors.New("something went wrong")
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var c Comment

		if err := cursor.Decode(&c); err != nil {
			return Comment{}, errors.New("something went wrong")
		}

		if strings.ToLower(strings.TrimSpace(c.Commentor)) == strings.ToLower(strings.TrimSpace(commentor)) && strings.ToLower(strings.TrimSpace(c.Comment)) == strings.ToLower(strings.TrimSpace(comment)) {
			return Comment{}, errors.New("you already made this reply")
		}
	}

	database_ID := primitive.NewObjectID()
	CommentId := database_ID.String()[10:34]
	belongsto := id
	return Comment{database_ID, CommentId, belongsto, commentor, comment, []Reply{}}, nil
}

func getNewReply(r *http.Request, id string) (Reply, error) {
	// validate form
	replier, exp := template.HTMLEscaper(r.FormValue("replier")), `^[a-zA-Z\s_]{2,35}$`
	if !valid(replier, exp) {
		return Reply{}, errors.New(`invalid input in name field or name not given, only "_" special character is allowed in name field a minimum of two characters and maximum of 35 characters`)
	}

	reply, exp := template.HTMLEscaper(r.FormValue("reply")), `.*`
	if !valid(reply, exp) {
		return Reply{}, errors.New("invalid input in reply field")
	}

	cursor, err := blogReplies.Find(ctx, bson.M{})
	if err != nil {
		return Reply{}, errors.New("something went wrong")
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var r Reply

		if err := cursor.Decode(&r); err != nil {
			return Reply{}, errors.New("something went wrong")
		}

		if strings.EqualFold(strings.TrimSpace(r.Replier), strings.TrimSpace(replier)) && strings.EqualFold(strings.TrimSpace(r.Reply), strings.TrimSpace(reply)) {
			return Reply{}, errors.New("you already made this reply")
		}

		// if strings.ToLower(strings.TrimSpace(r.Replier)) == strings.ToLower(strings.TrimSpace(replier)) && strings.ToLower(strings.TrimSpace(r.Reply)) == strings.ToLower(strings.TrimSpace(reply)) {
		// 	return Reply{}, errors.New("you already made this reply")
		// }
	}

	database_ID := primitive.NewObjectID()
	belongsto := id
	return Reply{database_ID, belongsto, replier, reply}, nil
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
	//fmt.Println(email)
	resp, err := http.Get(fmt.Sprintf("https://api.debounce.io/v1/?api=%s&email=%s", access_key, email))
	if err != nil {
		log.Println("Sending a GET on email validator api, may be due to poor internet connection")
		return errors.New("something went wrong, check your internet connection and try again later")
	}
	defer resp.Body.Close()

	bs, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("could not read email validator response body")
		return errors.New("something went wrong, check your internet connection and try again later")
	}

	//fmt.Println(string(bs))

	m := DeliverableEmail{}

	err = json.Unmarshal(bs, &m)
	if err != nil {
		log.Println("Email response body unmarshal:", err)
		return errors.New("something went wrong, check your internet connection and try again later")
	}

	fmt.Printf("Result: %v, Reason: %v\n", m.Result, m.Reason)

	// result is usually "Safe to Send" and Reason is usually "Deliverable" for registered/reachable emails
	if m.Result != "Safe to Send" || m.Reason != "Deliverable" {
		return errors.New("unregistered")
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
		return []string{}, errors.New("querying database failed")
	}
	defer cursor.Close(ctx)

	var emails []string

	for cursor.Next(ctx) {
		var sub Subscriber

		if err := cursor.Decode(&sub); err != nil {
			log.Println("Error getting subsciber email")
			continue
		}

		emails = append(emails, sub.Mail)
	}

	//fmt.Println(emails)

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
