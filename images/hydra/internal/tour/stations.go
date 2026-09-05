// Package tour 實作太魯閣線上導覽:站點資料、推進引擎、狀態存放與 SSE 端點
// (specs/006-taroko-tour;協定契約見該目錄 contracts/tour-http.md)。
package tour

// Station 站點靜態資料(specs/006 data-model.md);出處欄位對應 web/ATTRIBUTION.md。
type Station struct {
	Seq       int
	Name      string
	NameEN    string
	Desc      string
	Photo     string // web/photos/ 下的檔名
	Credit    string
	License   string
	SourceURL string
}

var stations = []Station{
	{
		Seq: 1, Name: "遊客中心", NameEN: "Taroko Visitor Center",
		Desc:  "太魯閣國家公園的導覽起點,提供園區地形與步道的完整介紹。從這裡出發,沿著立霧溪一路深入大理岩峽谷。",
		Photo: "visitor-center.jpg", Credit: "lienyuan lee", License: "CC BY 3.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:%E5%A4%AA%E9%AD%AF%E9%96%A3%E9%81%8A%E5%AE%A2%E4%B8%AD%E5%BF%83_Taroko_Visitor_Center_-_panoramio.jpg",
	},
	{
		Seq: 2, Name: "砂卡礑步道", NameEN: "Shakadang Trail",
		Desc:  "沿砂卡礑溪開鑿於岩壁上的平緩步道,溪水終年呈現獨特的藍綠色。褶皺大理岩與清澈溪流是這條步道的招牌風景。",
		Photo: "shakadang.jpg", Credit: "Zairon", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Taiwan_Taroko-Schlucht_Shakadang_Trail_37.jpg",
	},
	{
		Seq: 3, Name: "長春祠", NameEN: "Eternal Spring Shrine",
		Desc:  "為紀念中橫公路殉職人員而建,祠旁湧泉終年不歇、飛瀑直落立霧溪。白牆金頂倚著斷崖,是峽谷最經典的畫面之一。",
		Photo: "changchun.jpg", Credit: "Zairon", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Taiwan_Taroko-Schlucht_Eternal_Spring_Shrine_16.jpg",
	},
	{
		Seq: 4, Name: "燕子口", NameEN: "Swallow Grotto (Yanzikou)",
		Desc:  "立霧溪侵蝕出的壺穴地形密布岩壁,昔日燕群穿梭其間而得名。步道緊貼峽谷最窄處,抬頭僅見一線天。",
		Photo: "yanzikou.jpg", Credit: "黃庭富", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:6_DSC04487-Yanzikou.jpg",
	},
	{
		Seq: 5, Name: "九曲洞", NameEN: "Tunnel of Nine Turns (Jiuqudong)",
		Desc:  "中橫公路最險峻的一段,隧道在大理岩中蜿蜒九轉。步行其間,峽谷絕壁與溪谷深淵近在咫尺。",
		Photo: "jiuqudong.jpg", Credit: "Zairon", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Taiwan_Taroko-Schlucht_Tunnel_of_Nine_Turns_3.jpg",
	},
	{
		Seq: 6, Name: "白楊步道", NameEN: "Baiyang Trail",
		Desc:  "穿越多個隧道通往白楊瀑布,是園區最受歡迎的健行路線之一。終點的水簾洞湧泉自隧道頂傾瀉,如同天然水幕。",
		Photo: "baiyang.jpg", Credit: "Joyechen", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Baiyang_Trail_in_Taroko_National_Park.jpg",
	},
	{
		Seq: 7, Name: "清水斷崖", NameEN: "Qingshui Cliff",
		Desc:  "大理岩斷崖直落太平洋,落差近八百公尺,是蘇花公路最壯麗的一段。海水湛藍、崖壁如削,名列台灣八景。",
		Photo: "qingshui.jpg", Credit: "Huaiwun", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Taroko_National_Park_Qingshui_Cliff_(Huaiwun).jpg",
	},
}

// Stations 回傳全部站點(唯讀慣例:呼叫端不得修改)。
func Stations() []Station { return stations }

// 公告模式輪播文案(兼作 SSE 保活,見 specs/006 research R4)。
var notices = []string{
	"峽谷氣候多變,請隨身攜帶雨具並注意落石。",
	"部分步道需申請入園許可,行前請至官網確認開放狀態。",
	"夏季午後易有雷陣雨,行程請預留彈性。",
	"請愛護園區生態,不餵食、不帶走任何自然物。",
}

// NoticeText 依索引取得公告(循環)。
func NoticeText(i int) string { return notices[i%len(notices)] }

func noticeCount() int { return len(notices) }
