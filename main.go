package hdrezka

import (
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/PuerkitoBio/goquery"
)

// Версия библиотеки
const Version = "11.1.0"

// Типы контента
type (
    FormatType string
    CategoryType string
)

const (
    FormatTVSeries FormatType = "tv_series"
    FormatMovie    FormatType = "movie"
)

const (
    CategoryFilm    CategoryType = "films"
    CategorySeries  CategoryType = "series"
    CategoryCartoon CategoryType = "cartoons"
    CategoryAnime   CategoryType = "animation"
)

// Ошибки
type (
    LoginRequiredError struct{}
    LoginFailed       struct {
        Message string
    }
    FetchFailed struct{}
    CaptchaError struct{}
    HTTPError    struct {
        Code    int
        Message string
    }
)

func (e LoginRequiredError) Error() string {
    return "Login is required to access this page."
}

func (e LoginFailed) Error() string {
    return e.Message
}

func (e FetchFailed) Error() string {
    return "Failed to fetch stream!"
}

func (e CaptchaError) Error() string {
    return "Failed to bypass captcha!"
}

func (e HTTPError) Error() string {
    return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

// Рейтинг
type Rating struct {
    Value *float64
    Votes *int
}

func (r Rating) String() string {
    if r.Value == nil || r.Votes == nil {
        return "HdRezkaRating(Empty)"
    }
    return fmt.Sprintf("%.1f (%d)", *r.Value, *r.Votes)
}

func (r Rating) Float() float64 {
    if r.Value == nil {
        return 0
    }
    return *r.Value
}

func (r Rating) Int() int {
    if r.Value == nil {
        return 0
    }
    return int(*r.Value)
}

func (r Rating) IsEmpty() bool {
    return r.Value == nil || r.Votes == nil
}

// Переводчик
type Translator struct {
    ID      int
    Name    string
    Premium bool
}

// Поток видео
type Stream struct {
    Season        int
    Episode       int
    Name          string
    TranslatorID  int
    Subtitles     Subtitles
    Videos        map[string][]string // ключ - разрешение, значение - ссылки
}

func (s Stream) String() string {
    resolutions := make([]string, 0, len(s.Videos))
    for res := range s.Videos {
        resolutions = append(resolutions, res)
    }
    
    if len(s.Subtitles.Languages) > 0 {
        return fmt.Sprintf("<HdRezkaStream> : %v, subtitles=%v", resolutions, s.Subtitles)
    }
    return fmt.Sprintf("<HdRezkaStream> : %v", resolutions)
}

// Субтитры
type Subtitles struct {
    Languages map[string]SubtitleLanguage // ключ - код языка
    Keys      []string                     // коды языков
}

type SubtitleLanguage struct {
    Title string
    Link  string
}

func (s Subtitles) String() string {
    return fmt.Sprintf("%v", s.Keys)
}

func (s Subtitles) Get(id interface{}) (string, error) {
    if len(s.Languages) == 0 {
        return "", nil
    }
    
    switch v := id.(type) {
    case string:
        // Поиск по коду языка
        if sub, ok := s.Languages[v]; ok {
            return sub.Link, nil
        }
        
        // Поиск по названию языка
        for _, sub := range s.Languages {
            if sub.Title == v {
                return sub.Link, nil
            }
        }
        
        return "", fmt.Errorf("subtitles \"%v\" is not defined", id)
        
    case int:
        // Поиск по индексу
        if v < 0 || v >= len(s.Keys) {
            return "", fmt.Errorf("subtitles index %d out of range", v)
        }
        return s.Languages[s.Keys[v]].Link, nil
        
    default:
        return "", fmt.Errorf("invalid subtitle id type: %T", id)
    }
}

// Сезон и эпизоды
type Season struct {
    ID          int
    Title       string
    Episodes    []Episode
    Translations []Translator
}

type Episode struct {
    ID           int
    Title        string
    Translations []Translator
}

// Информация о сериале
type SeriesInfo struct {
    TranslatorID int
    TranslatorName string
    Premium      bool
    Seasons      map[int]string
    Episodes     map[int]map[int]string
}

// Результат поиска
type SearchResultItem struct {
    Title    string
    URL      string
    Image    string
    Category CategoryType
    Rating   *float64
}

// HdRezkaApi - основной API класс
type HdRezkaApi struct {
    url                     string
    origin                  string
    proxy                   *url.URL
    headers                 map[string]string
    cookies                 map[string]string
    translatorsPriority     []int
    translatorsNonPriority  []int
    
    // Кэшированные свойства
    mu             sync.Mutex
    id             *int
    name           *string
    names          []string
    origName       *string
    origNames      []string
    description    *string
    thumbnail      *string
    thumbnailHQ    *string
    releaseYear    *int
    formatType     *FormatType
    categoryType   *CategoryType
    rating         *Rating
    translators    map[int]Translator
    translatorsNames map[string]Translator
    otherParts     []map[string]string
    seriesInfo     map[int]SeriesInfo
    episodesInfo   []Season
}

func NewHdRezkaApi(rawURL string, options ...Option) (*HdRezkaApi, error) {
    parsedURL, err := url.Parse(rawURL)
    if err != nil {
        return nil, err
    }

    // Убедимся, что URL заканчивается на .html
    if !strings.HasSuffix(parsedURL.Path, ".html") {
        parsedURL.Path += ".html"
    }

    api := &HdRezkaApi{
        url:    parsedURL.String(),
        origin: fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host),
        headers: map[string]string{
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.138 Safari/537.36",
        },
        cookies: map[string]string{
            "hdmbbs": "1",
        },
        translatorsPriority: []int{
            56,   // Дубляж
            105,  // StudioBand
            111,  // HDrezka Studio
        },
        translatorsNonPriority: []int{
            238,  // Оригинал + субтитры
        },
    }

    // Применяем опции
    for _, option := range options {
        option(api)
    }

    return api, nil
}

type Option func(*HdRezkaApi)

func WithProxy(proxyURL string) Option {
    return func(api *HdRezkaApi) {
        if proxyURL != "" {
            parsed, err := url.Parse(proxyURL)
            if err == nil {
                api.proxy = parsed
            }
        }
    }
}

func WithHeaders(headers map[string]string) Option {
    return func(api *HdRezkaApi) {
        for k, v := range headers {
            api.headers[k] = v
        }
    }
}

func WithCookies(cookies map[string]string) Option {
    return func(api *HdRezkaApi) {
        for k, v := range cookies {
            api.cookies[k] = v
        }
    }
}

func WithTranslatorsPriority(priority []int) Option {
    return func(api *HdRezkaApi) {
        if len(priority) > 0 {
            api.translatorsPriority = priority
        }
    }
}

func WithTranslatorsNonPriority(nonPriority []int) Option {
    return func(api *HdRezkaApi) {
        if len(nonPriority) > 0 {
            api.translatorsNonPriority = nonPriority
        }
    }
}

func (api *HdRezkaApi) String() string {
    return fmt.Sprintf("HdRezka(\"%s\")", api.GetName())
}

func (api *HdRezkaApi) makeRequest(method, reqURL string, body io.Reader) (*http.Response, error) {
    client := &http.Client{}
    if api.proxy != nil {
        client.Transport = &http.Transport{
            Proxy: http.ProxyURL(api.proxy),
        }
    }

    req, err := http.NewRequest(method, reqURL, body)
    if err != nil {
        return nil, err
    }

    // Установка заголовков
    for k, v := range api.headers {
        req.Header.Set(k, v)
    }

    // Установка cookies
    cookieValues := make([]string, 0, len(api.cookies))
    for k, v := range api.cookies {
        cookieValues = append(cookieValues, fmt.Sprintf("%s=%s", k, v))
    }
    if len(cookieValues) > 0 {
        req.Header.Set("Cookie", strings.Join(cookieValues, "; "))
    }

    return client.Do(req)
}

func (api *HdRezkaApi) Login(email, password string) error {
    data := url.Values{}
    data.Set("login_name", email)
    data.Set("login_password", password)

    resp, err := api.makeRequest("POST", fmt.Sprintf("%s/ajax/login/", api.origin), strings.NewReader(data.Encode()))
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return &HTTPError{Code: resp.StatusCode, Message: resp.Status}
    }

    var result struct {
        Success bool   `json:"success"`
        Message string `json:"message"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return err
    }

    if !result.Success {
        return &LoginFailed{Message: result.Message}
    }

    // Обновляем cookies
    for _, cookie := range resp.Cookies() {
        api.cookies[cookie.Name] = cookie.Value
    }

    return nil
}

func (api *HdRezkaApi) MakeCookies(userID, passwordHash string) map[string]string {
    return map[string]string{
        "dle_user_id":  userID,
        "dle_password": passwordHash,
    }
}

func (api *HdRezkaApi) GetPage() (*goquery.Document, error) {
    resp, err := api.makeRequest("GET", api.url, nil)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, &HTTPError{Code: resp.StatusCode, Message: resp.Status}
    }

    doc, err := goquery.NewDocumentFromReader(resp.Body)
    if err != nil {
        return nil, err
    }

    // Проверка на требование входа
    title := doc.Find("title").Text()
    if title == "Sign In" {
        return nil, LoginRequiredError{}
    }
    if title == "Verify" {
        return nil, CaptchaError{}
    }

    return doc, nil
}

func (api *HdRezkaApi) GetID() (int, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.id != nil {
        return *api.id, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return 0, err
    }

    // Попытки получить ID из разных мест
    idStr := ""
    if sel := doc.Find("#post_id"); sel.Length() > 0 {
        idStr, _ = sel.Attr("value")
    } else if sel := doc.Find("#send-video-issue"); sel.Length() > 0 {
        idStr, _ = sel.Attr("data-id")
    } else if sel := doc.Find("#user-favorites-holder"); sel.Length() > 0 {
        idStr, _ = sel.Attr("data-post_id")
    }

    if idStr == "" {
        // Извлечь ID из URL
        parts := strings.Split(strings.TrimSuffix(api.url, ".html"), "-")
        if len(parts) > 0 {
            idStr = parts[len(parts)-1]
        }
    }

    id, err := strconv.Atoi(idStr)
    if err != nil {
        return 0, fmt.Errorf("failed to parse ID: %v", err)
    }

    api.id = &id
    return id, nil
}

func (api *HdRezkaApi) GetName() string {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.name != nil {
        return *api.name
    }

    names, err := api.GetNames()
    if err != nil || len(names) == 0 {
        return ""
    }

    api.name = &names[0]
    return *api.name
}

func (api *HdRezkaApi) GetNames() ([]string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.names) > 0 {
        return api.names, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return nil, err
    }

    text := doc.Find(".b-post__title").Text()
    if text == "" {
        return nil, fmt.Errorf("title not found")
    }

    names := strings.Split(text, "/")
    for i, name := range names {
        names[i] = strings.TrimSpace(name)
    }

    api.names = names
    return names, nil
}

func (api *HdRezkaApi) GetOrigName() string {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.origName != nil {
        return *api.origName
    }

    names, err := api.GetOrigNames()
    if err != nil || len(names) == 0 {
        return ""
    }

    api.origName = &names[len(names)-1]
    return *api.origName
}

func (api *HdRezkaApi) GetOrigNames() ([]string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.origNames) > 0 {
        return api.origNames, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return nil, err
    }

    sel := doc.Find(".b-post__origtitle")
    if sel.Length() == 0 {
        return []string{}, nil
    }

    text := sel.Text()
    names := strings.Split(text, "/")
    for i, name := range names {
        names[i] = strings.TrimSpace(name)
    }

    api.origNames = names
    return names, nil
}

func (api *HdRezkaApi) GetDescription() (string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.description != nil {
        return *api.description, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return "", err
    }

    text := doc.Find(".b-post__description_text").Text()
    if text == "" {
        return "", fmt.Errorf("description not found")
    }

    desc := strings.TrimSpace(text)
    api.description = &desc
    return desc, nil
}

func (api *HdRezkaApi) GetThumbnail() (string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.thumbnail != nil {
        return *api.thumbnail, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return "", err
    }

    src, exists := doc.Find(".b-sidecover img").Attr("src")
    if !exists {
        return "", fmt.Errorf("thumbnail not found")
    }

    api.thumbnail = &src
    return src, nil
}

func (api *HdRezkaApi) GetThumbnailHQ() (string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.thumbnailHQ != nil {
        return *api.thumbnailHQ, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return "", err
    }

    href, exists := doc.Find(".b-sidecover a").Attr("href")
    if !exists {
        return "", fmt.Errorf("thumbnail HQ not found")
    }

    api.thumbnailHQ = &href
    return href, nil
}

func (api *HdRezkaApi) GetReleaseYear() (int, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.releaseYear != nil {
        return *api.releaseYear, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return 0, err
    }

    href, exists := doc.Find(".b-content__main .b-post__info a[href*=\"/year/\"]").Attr("href")
    if !exists {
        return 0, fmt.Errorf("release year not found")
    }

    re := regexp.MustCompile(`(\d{4})`)
    matches := re.FindStringSubmatch(href)
    if len(matches) < 2 {
        return 0, fmt.Errorf("failed to parse year from URL")
    }

    year, err := strconv.Atoi(matches[1])
    if err != nil {
        return 0, fmt.Errorf("failed to parse year: %v", err)
    }

    api.releaseYear = &year
    return year, nil
}

func (api *HdRezkaApi) GetType() (FormatType, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.formatType != nil {
        return *api.formatType, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return "", err
    }

    contentType, exists := doc.Find("meta[property=\"og:type\"]").Attr("content")
    if !exists {
        return "", fmt.Errorf("content type not found")
    }

    var formatType FormatType
    switch contentType {
    case "video.tv_series":
        formatType = FormatTVSeries
    case "video.movie":
        formatType = FormatMovie
    default:
        formatType = FormatType(contentType)
    }

    api.formatType = &formatType
    return formatType, nil
}

func (api *HdRezkaApi) GetCategory() (CategoryType, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.categoryType != nil {
        return *api.categoryType, nil
    }

    parsedURL, err := url.Parse(api.url)
    if err != nil {
        return "", err
    }

    pathParts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
    if len(pathParts) == 0 {
        return "", fmt.Errorf("failed to determine category from URL")
    }

    var categoryType CategoryType
    switch pathParts[0] {
    case "films":
        categoryType = CategoryFilm
    case "series":
        categoryType = CategorySeries
    case "cartoons":
        categoryType = CategoryCartoon
    case "animation":
        categoryType = CategoryAnime
    default:
        categoryType = CategoryType(pathParts[0])
    }

    api.categoryType = &categoryType
    return categoryType, nil
}

func (api *HdRezkaApi) GetRating() (Rating, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if api.rating != nil {
        return *api.rating, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return Rating{}, err
    }

    wrapper := doc.Find(".b-post__rating")
    if wrapper.Length() == 0 {
        rating := Rating{Value: nil, Votes: nil}
        api.rating = &rating
        return rating, nil
    }

    ratingText := wrapper.Find(".num").Text()
    votesText := wrapper.Find(".votes").Text()
    votesText = strings.Trim(votesText, "()")

    ratingValue, err := strconv.ParseFloat(ratingText, 64)
    if err != nil {
        return Rating{}, fmt.Errorf("failed to parse rating: %v", err)
    }

    votes, err := strconv.Atoi(votesText)
    if err != nil {
        return Rating{}, fmt.Errorf("failed to parse votes: %v", err)
    }

    rating := Rating{Value: &ratingValue, Votes: &votes}
    api.rating = &rating
    return rating, nil
}

func (api *HdRezkaApi) GetTranslators() (map[int]Translator, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.translators) > 0 {
        return api.translators, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return nil, err
    }

    translators := make(map[int]Translator)
    translatorsList := doc.Find("#translators-list")

    if translatorsList.Length() > 0 {
        translatorsList.Children().Each(func(i int, s *goquery.Selection) {
            idStr, exists := s.Attr("data-translator_id")
            if !exists {
                return
            }

            id, err := strconv.Atoi(idStr)
            if err != nil {
                return
            }

            name := strings.TrimSpace(s.Text())
            premium := s.HasClass("b-prem_translator")

            img := s.Find("img")
            if img.Length() > 0 {
                if lang, exists := img.Attr("title"); exists && !strings.Contains(name, lang) {
                    name += fmt.Sprintf(" (%s)", lang)
                }
            }

            translators[id] = Translator{
                ID:      id,
                Name:    name,
                Premium: premium,
            }
        })
    }

    if len(translators) == 0 {
        // Автоопределение
        var translationName string
        table := doc.Find(".b-post__info")
        table.Find("tr").Each(func(i int, s *goquery.Selection) {
            text := s.Text()
            if strings.Contains(text, "переводе") {
                parts := strings.Split(text, "В переводе:")
                if len(parts) > 1 {
                    translationName = strings.TrimSpace(parts[1])
                }
            }
        })

        translationID := func() int {
            contentType, _ := api.GetType()
            var initCDNEvents string
            switch contentType {
            case FormatTVSeries:
                initCDNEvents = "initCDNSeriesEvents"
            case FormatMovie:
                initCDNEvents = "initCDNMoviesEvents"
            default:
                return 0
            }

            html, _ := doc.Html()
            parts := strings.Split(html, fmt.Sprintf("sof.tv.%s", initCDNEvents))
            if len(parts) < 2 {
                return 0
            }

            part := parts[1]
            idx := strings.Index(part, "{")
            if idx == -1 {
                return 0
            }

            data := part[:idx]
            values := strings.Split(data, ",")
            if len(values) < 2 {
                return 0
            }

            id, err := strconv.Atoi(strings.TrimSpace(values[1]))
            if err != nil {
                return 0
            }

            return id
        }()

        if translationID > 0 {
            translators[translationID] = Translator{
                ID:      translationID,
                Name:    translationName,
                Premium: false,
            }
        }
    }

    api.translators = translators
    return translators, nil
}

func (api *HdRezkaApi) GetTranslatorsNames() (map[string]Translator, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.translatorsNames) > 0 {
        return api.translatorsNames, nil
    }

    translators, err := api.GetTranslators()
    if err != nil {
        return nil, err
    }

    names := make(map[string]Translator)
    for id, trans := range translators {
        names[trans.Name] = Translator{
            ID:      id,
            Name:    trans.Name,
            Premium: trans.Premium,
        }
    }

    api.translatorsNames = names
    return names, nil
}

func (api *HdRezkaApi) SortTranslators(translators map[int]Translator, priority, nonPriority []int) map[int]Translator {
    prior := make(map[int]int)
    for i, item := range priority {
        prior[item] = i + 1
    }

    maxIndex := len(prior) + 1
    for i, item := range nonPriority {
        if _, exists := prior[item]; !exists {
            prior[item] = maxIndex + i + 1
        }
    }

    sorted := make(map[int]Translator)
    ids := make([]int, 0, len(translators))
    for id := range translators {
        ids = append(ids, id)
    }

    // Сортировка по приоритету
    for _, id := range ids {
        priority := prior[id]
        if priority == 0 {
            priority = maxIndex
        }
        sorted[id] = translators[id]
    }

    return sorted
}

func (api *HdRezkaApi) ClearTrash(data string) (string, error) {
    trashList := []string{"@", "#", "!", "^", "$"}
    var trashCodes [][]byte

    // Генерация комбинаций мусорных кодов
    for i := 2; i <= 3; i++ {
        for _, chars := range product(trashList, i) {
            dataBytes := []byte(strings.Join(chars, ""))
            trashCombo := base64.StdEncoding.EncodeToString(dataBytes)
            trashCodes = append(trashCodes, []byte(trashCombo))
        }
    }

    arr := strings.ReplaceAll(data, "#h", "")
    parts := strings.Split(arr, "//_//")
    trashString := strings.Join(parts, "")

    for _, code := range trashCodes {
        trashString = strings.ReplaceAll(trashString, string(code), "")
    }

    decoded, err := base64.StdEncoding.DecodeString(trashString + "==")
    if err != nil {
        return "", fmt.Errorf("failed to decode base64: %v", err)
    }

    return string(decoded), nil
}

func product(elements []string, repeat int) [][]string {
    if repeat == 1 {
        result := make([][]string, len(elements))
        for i, e := range elements {
            result[i] = []string{e}
        }
        return result
    }

    var result [][]string
    for _, e := range elements {
        for _, p := range product(elements, repeat-1) {
            result = append(result, append([]string{e}, p...))
        }
    }
    return result
}

func (api *HdRezkaApi) GetOtherParts() ([]map[string]string, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.otherParts) > 0 {
        return api.otherParts, nil
    }

    doc, err := api.GetPage()
    if err != nil {
        return nil, err
    }

    var parts []map[string]string
    partsContainer := doc.Find(".b-post__partcontent")

    if partsContainer.Length() > 0 {
        partsContainer.Find(".b-post__partcontent_item").Each(func(i int, s *goquery.Selection) {
            title := s.Find(".title").Text()
            if s.HasClass("current") {
                parts = append(parts, map[string]string{title: api.url})
            } else {
                url, exists := s.Attr("data-url")
                if exists {
                    parts = append(parts, map[string]string{title: url})
                }
            }
        })
    }

    api.otherParts = parts
    return parts, nil
}

func (api *HdRezkaApi) GetEpisodes(seasonsHTML, episodesHTML string) (map[int]string, map[int]map[int]string, error) {
    seasonsDoc, err := goquery.NewDocumentFromReader(strings.NewReader(seasonsHTML))
    if err != nil {
        return nil, nil, err
    }

    episodesDoc, err := goquery.NewDocumentFromReader(strings.NewReader(episodesHTML))
    if err != nil {
        return nil, nil, err
    }

    seasons := make(map[int]string)
    seasonsDoc.Find(".b-simple_season__item").Each(func(i int, s *goquery.Selection) {
        idStr, exists := s.Attr("data-tab_id")
        if !exists {
            return
        }

        id, err := strconv.Atoi(idStr)
        if err != nil {
            return
        }

        seasons[id] = s.Text()
    })

    episodes := make(map[int]map[int]string)
    episodesDoc.Find(".b-simple_episode__item").Each(func(i int, s *goquery.Selection) {
        seasonIDStr, exists := s.Attr("data-season_id")
        if !exists {
            return
        }

        episodeIDStr, exists := s.Attr("data-episode_id")
        if !exists {
            return
        }

        seasonID, err := strconv.Atoi(seasonIDStr)
        if err != nil {
            return
        }

        episodeID, err := strconv.Atoi(episodeIDStr)
        if err != nil {
            return
        }

        if _, exists := episodes[seasonID]; !exists {
            episodes[seasonID] = make(map[int]string)
        }

        episodes[seasonID][episodeID] = s.Text()
    })

    return seasons, episodes, nil
}

func (api *HdRezkaApi) GetSeriesInfo() (map[int]SeriesInfo, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.seriesInfo) > 0 {
        return api.seriesInfo, nil
    }

    contentType, err := api.GetType()
    if err != nil {
        return nil, err
    }

    if contentType != FormatTVSeries {
        return nil, fmt.Errorf("series info is only available for TV series")
    }

    translators, err := api.GetTranslators()
    if err != nil {
        return nil, err
    }

    id, err := api.GetID()
    if err != nil {
        return nil, err
    }

    seriesInfo := make(map[int]SeriesInfo)
    for trID, trVal := range translators {
        data := url.Values{}
        data.Set("id", strconv.Itoa(id))
        data.Set("translator_id", strconv.Itoa(trID))
        data.Set("action", "get_episodes")

        resp, err := api.makeRequest("POST", fmt.Sprintf("%s/ajax/get_cdn_series/", api.origin), strings.NewReader(data.Encode()))
        if err != nil {
            continue
        }
        defer resp.Body.Close()

        if resp.StatusCode != http.StatusOK {
            continue
        }

        var result struct {
            Success  bool   `json:"success"`
            Seasons  string `json:"seasons"`
            Episodes string `json:"episodes"`
        }

        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
            continue
        }

        if !result.Success {
            continue
        }

        seasons, episodes, err := api.GetEpisodes(result.Seasons, result.Episodes)
        if err != nil {
            continue
        }

        seriesInfo[trID] = SeriesInfo{
            TranslatorID:   trID,
            TranslatorName: trVal.Name,
            Premium:       trVal.Premium,
            Seasons:       seasons,
            Episodes:      episodes,
        }
    }

    api.seriesInfo = seriesInfo
    return seriesInfo, nil
}

func (api *HdRezkaApi) GetEpisodesInfo() ([]Season, error) {
    api.mu.Lock()
    defer api.mu.Unlock()

    if len(api.episodesInfo) > 0 {
        return api.episodesInfo, nil
    }

    contentType, err := api.GetType()
    if err != nil {
        return nil, err
    }

    if contentType != FormatTVSeries {
        return nil, fmt.Errorf("episodes info is only available for TV series")
    }

    seriesInfo, err := api.GetSeriesInfo()
    if err != nil {
        return nil, err
    }

    seasonsMap := make(map[int]*Season)
    for trID, info := range seriesInfo {
        for seasonID, seasonText := range info.Seasons {
            if _, exists := seasonsMap[seasonID]; !exists {
                seasonsMap[seasonID] = &Season{
                    ID:   seasonID,
                    Title: seasonText,
                }
            }

            for episodeID, episodeText := range info.Episodes[seasonID] {
                found := false
                for i, ep := range seasonsMap[seasonID].Episodes {
                    if ep.ID == episodeID {
                        // Добавляем переводчик к существующему эпизоду
                        seasonsMap[seasonID].Episodes[i].Translations = append(
                            seasonsMap[seasonID].Episodes[i].Translations,
                            Translator{
                                ID:      trID,
                                Name:    info.TranslatorName,
                                Premium: info.Premium,
                            },
                        )
                        found = true
                        break
                    }
                }

                if !found {
                    // Создаем новый эпизод
                    seasonsMap[seasonID].Episodes = append(seasonsMap[seasonID].Episodes, Episode{
                        ID:    episodeID,
                        Title: episodeText,
                        Translations: []Translator{
                            {
                                ID:      trID,
                                Name:    info.TranslatorName,
                                Premium: info.Premium,
                            },
                        },
                    })
                }
            }
        }
    }

    // Преобразуем map в slice
    var seasons []Season
    for _, season := range seasonsMap {
        seasons = append(seasons, *season)
    }

    api.episodesInfo = seasons
    return seasons, nil
}

func (api *HdRezkaApi) GetStream(season, episode, translation int, priority, nonPriority []int) (*Stream, error) {
    contentType, err := api.GetType()
    if err != nil {
        return nil, err
    }

    id, err := api.GetID()
    if err != nil {
        return nil, err
    }

    var translatorID int
    if contentType == FormatTVSeries {
        if season == 0 || episode == 0 {
            return nil, fmt.Errorf("season and episode are required for TV series")
        }

        // Получаем информацию об эпизодах
        episodesInfo, err := api.GetEpisodesInfo()
        if err != nil {
            return nil, err
        }

        // Ищем сезон
        var seasonData *Season
        for _, s := range episodesInfo {
            if s.ID == season {
                seasonData = &s
                break
            }
        }

        if seasonData == nil {
            return nil, fmt.Errorf("season %d not found", season)
        }

        // Ищем эпизод
        var episodeData *Episode
        for _, e := range seasonData.Episodes {
            if e.ID == episode {
                episodeData = &e
                break
            }
        }

        if episodeData == nil {
            return nil, fmt.Errorf("episode %d in season %d not found", episode, season)
        }

        // Определяем переводчик
        if translation != 0 {
            found := false
            for _, t := range episodeData.Translations {
                if t.ID == translation {
                    translatorID = t.ID
                    found = true
                    break
                }
            }
            if !found {
                return nil, fmt.Errorf("translator with ID %d not found for this episode", translation)
            }
        } else {
            // Используем приоритет
            translators := make(map[int]Translator)
            for _, t := range episodeData.Translations {
                translators[t.ID] = t
            }

            sorted := api.SortTranslators(translators, priority, nonPriority)
            if len(sorted) == 0 {
                return nil, fmt.Errorf("no translators available")
            }

            // Берем первый из отсортированных
            for id := range sorted {
                translatorID = id
                break
            }
        }

        // Запрашиваем поток
        data := url.Values{}
        data.Set("id", strconv.Itoa(id))
        data.Set("translator_id", strconv.Itoa(translatorID))
        data.Set("season", strconv.Itoa(season))
        data.Set("episode", strconv.Itoa(episode))
        data.Set("action", "get_stream")

        return api.makeStreamRequest(data)
    } else if contentType == FormatMovie {
        translators, err := api.GetTranslators()
        if err != nil {
            return nil, err
        }

        // Определяем переводчик
        if translation != 0 {
            if _, exists := translators[translation]; !exists {
                return nil, fmt.Errorf("translator with ID %d not found", translation)
            }
            translatorID = translation
        } else {
            // Используем приоритет
            sorted := api.SortTranslators(translators, priority, nonPriority)
            if len(sorted) == 0 {
                return nil, fmt.Errorf("no translators available")
            }

            // Берем первый из отсортированных
            for id := range sorted {
                translatorID = id
                break
            }
        }

        // Запрашиваем поток
        data := url.Values{}
        data.Set("id", strconv.Itoa(id))
        data.Set("translator_id", strconv.Itoa(translatorID))
        data.Set("action", "get_movie")

        return api.makeStreamRequest(data)
    }

    return nil, fmt.Errorf("unsupported content type")
}

func (api *HdRezkaApi) makeStreamRequest(data url.Values) (*Stream, error) {
    resp, err := api.makeRequest("POST", fmt.Sprintf("%s/ajax/get_cdn_series/", api.origin), strings.NewReader(data.Encode()))
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, &HTTPError{Code: resp.StatusCode, Message: resp.Status}
    }

    var result struct {
        Success    bool              `json:"success"`
        URL        string            `json:"url"`
        Subtitle   string            `json:"subtitle"`
        SubtitleLns map[string]string `json:"subtitle_lns"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    if !result.Success || result.URL == "" {
        return nil, FetchFailed{}
    }

    // Очищаем URL от мусора
    cleanURL, err := api.ClearTrash(result.URL)
    if err != nil {
        return nil, fmt.Errorf("failed to clean stream URL: %v", err)
    }

    // Парсим потоки
    stream := &Stream{
        Videos:       make(map[string][]string),
        TranslatorID: mustAtoi(data.Get("translator_id")),
        Subtitles: Subtitles{
            Languages: make(map[string]SubtitleLanguage),
        },
    }

    season := data.Get("season")
    if season != "" {
        stream.Season = mustAtoi(season)
    }

    episode := data.Get("episode")
    if episode != "" {
        stream.Episode = mustAtoi(episode)
    }

    name := api.GetName()
    stream.Name = name

    // Обрабатываем субтитры
    if result.Subtitle != "" {
        parts := strings.Split(result.Subtitle, ",")
        for _, part := range parts {
            re := regexp.MustCompile(`\[([^\]]+)\]([^\[]+)`)
            matches := re.FindStringSubmatch(part)
            if len(matches) < 3 {
                continue
            }

            lang := matches[1]
            link := matches[2]

            code, exists := result.SubtitleLns[lang]
            if !exists {
                continue
            }

            stream.Subtitles.Languages[code] = SubtitleLanguage{
                Title: lang,
                Link:  link,
            }
            stream.Subtitles.Keys = append(stream.Subtitles.Keys, code)
        }
    }

    // Обрабатываем видео потоки
    urlParts := strings.Split(cleanURL, ",")
    for _, part := range urlParts {
        re := regexp.MustCompile(`\[([^\]]+)\]([^\[]+)`)
        matches := re.FindStringSubmatch(part)
        if len(matches) < 3 {
            continue
        }

        quality := matches[1]
        links := strings.Split(matches[2], " or ")

        for _, link := range links {
            if strings.HasSuffix(link, ".mp4") {
                stream.Videos[quality] = append(stream.Videos[quality], link)
            }
        }
    }

    return stream, nil
}

func mustAtoi(s string) int {
    val, err := strconv.Atoi(s)
    if err != nil {
        return 0
    }
    return val
}

func (api *HdRezkaApi) GetSeasonStreams(season int, translation int, priority, nonPriority []int, ignore bool, progressFunc func(current, total int)) (map[int]*Stream, error) {
    contentType, err := api.GetType()
    if err != nil {
        return nil, err
    }

    if contentType != FormatTVSeries {
        return nil, fmt.Errorf("season streams are only available for TV series")
    }

    episodesInfo, err := api.GetEpisodesInfo()
    if err != nil {
        return nil, err
    }

    // Ищем сезон
    var seasonData *Season
    for _, s := range episodesInfo {
        if s.ID == season {
            seasonData = &s
            break
        }
    }

    if seasonData == nil {
        return nil, fmt.Errorf("season %d not found", season)
    }

    // Определяем переводчик
    var translatorID int
    if translation != 0 {
        found := false
        for _, t := range seasonData.Translations {
            if t.ID == translation {
                translatorID = t.ID
                found = true
                break
            }
        }
        if !found {
            return nil, fmt.Errorf("translator with ID %d not found for this season", translation)
        }
    } else {
        // Используем приоритет
        translators := make(map[int]Translator)
        for _, t := range seasonData.Translations {
            translators[t.ID] = t
        }

        sorted := api.SortTranslators(translators, priority, nonPriority)
        if len(sorted) == 0 {
            return nil, fmt.Errorf("no translators available")
        }

        // Берем первый из отсортированных
        for id := range sorted {
            translatorID = id
            break
        }
    }

    // Собираем эпизоды для этого переводчика
    var episodes []int
    for _, ep := range seasonData.Episodes {
        for _, t := range ep.Translations {
            if t.ID == translatorID {
                episodes = append(episodes, ep.ID)
                break
            }
        }
    }

    if len(episodes) == 0 {
        return nil, fmt.Errorf("no episodes found for translator %d", translatorID)
    }

    // Функция прогресса по умолчанию
    if progressFunc == nil {
        progressFunc = func(current, total int) {}
    }

    streams := make(map[int]*Stream)
    total := len(episodes)
    progressFunc(0, total)

    for i, episodeID := range episodes {
        var stream *Stream
        var err error

        // Пытаемся получить поток с повторной попыткой
        for attempt := 0; attempt < 2; attempt++ {
            stream, err = api.GetStream(season, episodeID, translatorID, priority, nonPriority)
            if err == nil {
                break
            }

            if attempt == 0 && ignore {
                // Небольшая задержка перед повторной попыткой
                time.Sleep(time.Second)
            }
        }

        if err != nil {
            if !ignore {
                return nil, fmt.Errorf("failed to get stream for episode %d: %v", episodeID, err)
            }
            // В режиме игнорирования ошибок просто пропускаем
            continue
        }

        streams[episodeID] = stream
        progressFunc(i+1, total)
    }

    return streams, nil
}

// HdRezkaSearch - класс для поиска
type HdRezkaSearch struct {
    origin  string
    proxy   *url.URL
    headers map[string]string
    cookies map[string]string
}

func NewHdRezkaSearch(origin string, options ...SearchOption) *HdRezkaSearch {
    search := &HdRezkaSearch{
        origin: origin,
        headers: map[string]string{
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.138 Safari/537.36",
        },
        cookies: map[string]string{
            "hdmbbs": "1",
        },
    }

    // Применяем опции
    for _, option := range options {
        option(search)
    }

    return search
}

type SearchOption func(*HdRezkaSearch)

func SearchWithProxy(proxyURL string) SearchOption {
    return func(s *HdRezkaSearch) {
        if proxyURL != "" {
            parsed, err := url.Parse(proxyURL)
            if err == nil {
                s.proxy = parsed
            }
        }
    }
}

func SearchWithHeaders(headers map[string]string) SearchOption {
    return func(s *HdRezkaSearch) {
        for k, v := range headers {
            s.headers[k] = v
        }
    }
}

func SearchWithCookies(cookies map[string]string) SearchOption {
    return func(s *HdRezkaSearch) {
        for k, v := range cookies {
            s.cookies[k] = v
        }
    }
}

func (s *HdRezkaSearch) FastSearch(query string) ([]SearchResultItem, error) {
    data := url.Values{}
    data.Set("q", query)

    client := &http.Client{}
    if s.proxy != nil {
        client.Transport = &http.Transport{
            Proxy: http.ProxyURL(s.proxy),
        }
    }

    req, err := http.NewRequest("POST", fmt.Sprintf("%s/engine/ajax/search.php", s.origin), strings.NewReader(data.Encode()))
    if err != nil {
        return nil, err
    }

    // Установка заголовков
    for k, v := range s.headers {
        req.Header.Set(k, v)
    }

    // Установка cookies
    cookieValues := make([]string, 0, len(s.cookies))
    for k, v := range s.cookies {
        cookieValues = append(cookieValues, fmt.Sprintf("%s=%s", k, v))
    }
    if len(cookieValues) > 0 {
        req.Header.Set("Cookie", strings.Join(cookieValues, "; "))
    }

    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, &HTTPError{Code: resp.StatusCode, Message: resp.Status}
    }

    doc, err := goquery.NewDocumentFromReader(resp.Body)
    if err != nil {
        return nil, err
    }

    var results []SearchResultItem
    doc.Find(".b-search__section_list li").Each(func(i int, sel *goquery.Selection) {
        title := sel.Find("span.enty").Text()
        title = strings.TrimSpace(title)

        url, exists := sel.Find("a").Attr("href")
        if !exists {
            return
        }

        ratingText := sel.Find("span.rating").Text()
        var rating *float64
        if ratingText != "" {
            val, err := strconv.ParseFloat(ratingText, 64)
            if err == nil {
                rating = &val
            }
        }

        results = append(results, SearchResultItem{
            Title:  title,
            URL:    url,
            Rating: rating,
        })
    })

    return results, nil
}

func (s *HdRezkaSearch) AdvancedSearch(query string) *SearchResult {
    return &SearchResult{
        origin:  s.origin,
        query:   query,
        proxy:   s.proxy,
        headers: s.headers,
        cookies: s.cookies,
    }
}

// SearchResult - результаты расширенного поиска
type SearchResult struct {
    origin  string
    query   string
    proxy   *url.URL
    headers map[string]string
    cookies map[string]string
}

func (sr *SearchResult) String() string {
    return fmt.Sprintf("SearchResult(%s)", sr.query)
}

func (sr *SearchResult) Len() int {
    all, err := sr.GetAll()
    if err != nil {
        return 0
    }
    return len(all)
}

func (sr *SearchResult) GetAll() ([]SearchResultItem, error) {
    var allResults []SearchResultItem
    
    page := 1
    for {
        results, err := sr.GetPage(page)
        if err != nil {
            if page == 1 {
                return nil, err
            }
            break
        }
        
        if len(results) == 0 {
            break
        }
        
        allResults = append(allResults, results...)
        page++
    }
    
    return allResults, nil
}

func (sr *SearchResult) GetPage(page int) ([]SearchResultItem, error) {
    params := url.Values{}
    params.Set("do", "search")
    params.Set("subaction", "search")
    params.Set("q", sr.query)
    params.Set("page", strconv.Itoa(page))

    client := &http.Client{}
    if sr.proxy != nil {
        client.Transport = &http.Transport{
            Proxy: http.ProxyURL(sr.proxy),
        }
    }

    reqURL := fmt.Sprintf("%s/search/?%s", sr.origin, params.Encode())
    req, err := http.NewRequest("GET", reqURL, nil)
    if err != nil {
        return nil, err
    }

    // Установка заголовков
    for k, v := range sr.headers {
        req.Header.Set(k, v)
    }

    // Установка cookies
    cookieValues := make([]string, 0, len(sr.cookies))
    for k, v := range sr.cookies {
        cookieValues = append(cookieValues, fmt.Sprintf("%s=%s", k, v))
    }
    if len(cookieValues) > 0 {
        req.Header.Set("Cookie", strings.Join(cookieValues, "; "))
    }

    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, &HTTPError{Code: resp.StatusCode, Message: resp.Status}
    }

    doc, err := goquery.NewDocumentFromReader(resp.Body)
    if err != nil {
        return nil, err
    }

    // Проверка на требование входа
    title := doc.Find("title").Text()
    if title == "Sign In" {
        return nil, LoginRequiredError{}
    }
    if title == "Verify" {
        return nil, CaptchaError{}
    }

    var results []SearchResultItem
    doc.Find(".b-content__inline_item").Each(func(i int, sel *goquery.Selection) {
        link := sel.Find(".b-content__inline_item-link a")
        title := link.Text()
        title = strings.TrimSpace(title)

        url, exists := link.Attr("href")
        if !exists {
            return
        }

        cover := sel.Find(".b-content__inline_item-cover img")
        image, exists := cover.Attr("src")
        if !exists {
            return
        }

        cat := sel.Find(".cat")
        var category CategoryType
        if cat.Length() > 0 {
            classes := cat.Nodes[0].Attr
            for _, attr := range classes {
                if attr.Key == "class" {
                    classList := strings.Fields(attr.Val)
                    for _, class := range classList {
                        if class == "cat" {
                            continue
                        }
                        
                        switch class {
                        case "films":
                            category = CategoryFilm
                        case "series":
                            category = CategorySeries
                        case "cartoons":
                            category = CategoryCartoon
                        case "animation":
                            category = CategoryAnime
                        default:
                            category = CategoryType(class)
                        }
                        break
                    }
                    break
                }
            }
        }

        results = append(results, SearchResultItem{
            Title:    title,
            URL:      url,
            Image:    image,
            Category: category,
        })
    })

    return results, nil
}

// HdRezkaSession - класс для управления сессией
type HdRezkaSession struct {
    origin                 string
    proxy                  *url.URL
    headers                map[string]string
    cookies                map[string]string
    translatorsPriority    []int
    translatorsNonPriority []int
}

func NewHdRezkaSession(origin string, options ...SessionOption) *HdRezkaSession {
    session := &HdRezkaSession{
        headers: map[string]string{
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.138 Safari/537.36",
        },
        cookies: map[string]string{
            "hdmbbs": "1",
        },
        translatorsPriority: []int{
            56,   // Дубляж
            105,  // StudioBand
            111,  // HDrezka Studio
        },
        translatorsNonPriority: []int{
            238,  // Оригинал + субтитры
        },
    }

    if origin != "" {
        parsedURL, err := url.Parse(origin)
        if err == nil {
            session.origin = fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
        }
    }

    // Применяем опции
    for _, option := range options {
        option(session)
    }

    return session
}

type SessionOption func(*HdRezkaSession)

func SessionWithProxy(proxyURL string) SessionOption {
    return func(s *HdRezkaSession) {
        if proxyURL != "" {
            parsed, err := url.Parse(proxyURL)
            if err == nil {
                s.proxy = parsed
            }
        }
    }
}

func SessionWithHeaders(headers map[string]string) SessionOption {
    return func(s *HdRezkaSession) {
        for k, v := range headers {
            s.headers[k] = v
        }
    }
}

func SessionWithCookies(cookies map[string]string) SessionOption {
    return func(s *HdRezkaSession) {
        for k, v := range cookies {
            s.cookies[k] = v
        }
    }
}

func SessionWithTranslatorsPriority(priority []int) SessionOption {
    return func(s *HdRezkaSession) {
        if len(priority) > 0 {
            s.translatorsPriority = priority
        }
    }
}

func SessionWithTranslatorsNonPriority(nonPriority []int) SessionOption {
    return func(s *HdRezkaSession) {
        if len(nonPriority) > 0 {
            s.translatorsNonPriority = nonPriority
        }
    }
}

func (s *HdRezkaSession) Login(email, password string) error {
    if s.origin == "" {
        return fmt.Errorf("origin is required for login")
    }

    // Создаем API без прокси через опции
    api, err := NewHdRezkaApi(s.origin, 
        WithHeaders(s.headers),
        WithCookies(s.cookies),
    )
    if err != nil {
        return err
    }

    // Устанавливаем прокси напрямую, если он есть
    if s.proxy != nil {
        api.proxy = s.proxy
    }

    if err := api.Login(email, password); err != nil {
        return err
    }

    // Обновляем cookies в сессии
    for k, v := range api.cookies {
        s.cookies[k] = v
    }

    return nil
}

func (s *HdRezkaSession) Get(rawURL string) (*HdRezkaApi, error) {
    var urlToUse string
    
    if s.origin != "" {
        parsedURL, err := url.Parse(rawURL)
        if err != nil {
            return nil, err
        }
        
        // Если URL относительный, добавляем origin
        if !parsedURL.IsAbs() {
            urlToUse = s.origin + "/" + strings.TrimPrefix(parsedURL.Path, "/")
        } else {
            urlToUse = rawURL
        }
    } else {
        urlToUse = rawURL
    }

    // Создаем API без прокси через опции
    api, err := NewHdRezkaApi(urlToUse,
        WithHeaders(s.headers),
        WithCookies(s.cookies),
        WithTranslatorsPriority(s.translatorsPriority),
        WithTranslatorsNonPriority(s.translatorsNonPriority),
    )
    if err != nil {
        return nil, err
    }

    // Устанавливаем прокси напрямую, если он есть
    if s.proxy != nil {
        api.proxy = s.proxy
    }

    // Проверяем, что страница доступна
    if _, err := api.GetPage(); err != nil {
        return nil, err
    }

    return api, nil
}

func (s *HdRezkaSession) Search(query string, findAll bool) (interface{}, error) {
    if s.origin == "" {
        return nil, fmt.Errorf("origin is required for search")
    }

    // Создаем поиск без прокси через опции
    search := NewHdRezkaSearch(s.origin,
        SearchWithHeaders(s.headers),
        SearchWithCookies(s.cookies),
    )

    // Устанавливаем прокси напрямую, если он есть
    if s.proxy != nil {
        search.proxy = s.proxy
    }

    if findAll {
        return search.AdvancedSearch(query), nil
    }
    
    return search.FastSearch(query)
}
