// Package nginxaccess 提供 Nginx access log 的地理聚合服务：
// Agent 上报的 Top-N 来源 IP 经内置 IP 归属库映射为省份/国家，
// 在内存滑动窗口中聚合，供数据大屏 Nginx 分析板块展示。
package nginxaccess

import (
	_ "embed"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

//go:embed data/ip2region_v4.xdb
var ipDB []byte

// Geo 提供 IP 归属地查询（全内存加载，并发安全，首次查询时惰性初始化）。
type Geo struct {
	searcher *xdb.Searcher
	once     sync.Once
	err      error
}

// NewGeo 创建 Geo 查询器。
func NewGeo() *Geo { return &Geo{} }

// Search 查询 IP 归属地，返回国家（中文名）、国家（英文原名，用于世界地图匹配）、
// 省份（中文简称，去行政后缀）、城市。查询失败或无法识别时返回空字符串。
func (g *Geo) Search(ip string) (country, countryEn, province, city string) {
	g.once.Do(func() {
		g.searcher, g.err = xdb.NewWithBuffer(xdb.IPv4, ipDB)
	})
	if g.err != nil || g.searcher == nil {
		return "", "", "", ""
	}
	region, err := g.searcher.Search(ip)
	if err != nil {
		return "", "", "", ""
	}
	parts := strings.Split(region, "|")
	if len(parts) >= 1 {
		country = countryCN(parts[0])
		// 国内 IP 段国家字段为中文（"中国"），转英文便于世界地图匹配
		if parts[0] == "中国" {
			countryEn = "China"
		} else {
			countryEn = strings.TrimSpace(parts[0])
		}
	}
	if len(parts) >= 3 {
		province = normProvince(parts[2])
	}
	if len(parts) >= 4 && parts[3] != "" && parts[3] != "0" {
		city = parts[3]
	}
	return
}

// countryMap 国家英文名 → 中文名（对齐世界地图 GeoJSON 的 name 字段）。
var countryMap = map[string]string{
	"China": "中国", "United States": "美国", "Japan": "日本", "Korea": "韩国", "South Korea": "韩国",
	"North Korea": "朝鲜", "Germany": "德国", "France": "法国", "United Kingdom": "英国",
	"Canada": "加拿大", "Australia": "澳大利亚", "India": "印度", "Russia": "俄罗斯",
	"Brazil": "巴西", "Singapore": "新加坡", "Malaysia": "马来西亚", "Thailand": "泰国",
	"Vietnam": "越南", "Indonesia": "印度尼西亚", "Philippines": "菲律宾", "Italy": "意大利",
	"Spain": "西班牙", "Netherlands": "荷兰", "Poland": "波兰", "Sweden": "瑞典",
	"Norway": "挪威", "Finland": "芬兰", "Denmark": "丹麦", "Switzerland": "瑞士",
	"Austria": "奥地利", "Belgium": "比利时", "Portugal": "葡萄牙", "Turkey": "土耳其",
	"Egypt": "埃及", "South Africa": "南非", "Nigeria": "尼日利亚", "Kenya": "肯尼亚",
	"Saudi Arabia": "沙特阿拉伯", "United Arab Emirates": "阿联酋", "Israel": "以色列",
	"Iran": "伊朗", "Iraq": "伊拉克", "Pakistan": "巴基斯坦", "Bangladesh": "孟加拉国",
	"Sri Lanka": "斯里兰卡", "Nepal": "尼泊尔", "Afghanistan": "阿富汗",
	"Kazakhstan": "哈萨克斯坦", "Ukraine": "乌克兰", "Belarus": "白俄罗斯",
	"Romania": "罗马尼亚", "Czech Republic": "捷克", "Greece": "希腊", "Ireland": "爱尔兰",
	"New Zealand": "新西兰", "Argentina": "阿根廷", "Chile": "智利", "Colombia": "哥伦比亚",
	"Mexico": "墨西哥", "Venezuela": "委内瑞拉", "Peru": "秘鲁", "Mongolia": "蒙古",
	"Luxembourg": "卢森堡", "Qatar": "卡塔尔", "Kuwait": "科威特", "Oman": "阿曼",
	"Bahrain": "巴林", "Jordan": "约旦", "Lebanon": "黎巴嫩", "Myanmar": "缅甸",
	"Cambodia": "柬埔寨", "Laos": "老挝", "Bhutan": "不丹", "Maldives": "马尔代夫",
	"Taiwan": "中国台湾", "Hong Kong": "中国香港", "Macau": "中国澳门", "Hungary": "匈牙利",
	"Bulgaria": "保加利亚", "Serbia": "塞尔维亚", "Croatia": "克罗地亚",
	"Slovenia": "斯洛文尼亚", "Slovakia": "斯洛伐克", "Lithuania": "立陶宛",
	"Latvia": "拉脱维亚", "Estonia": "爱沙尼亚", "Georgia": "格鲁吉亚",
	"Armenia": "亚美尼亚", "Azerbaijan": "阿塞拜疆", "Uzbekistan": "乌兹别克斯坦",
	"Turkmenistan": "土库曼斯坦", "Kyrgyzstan": "吉尔吉斯斯坦", "Tajikistan": "塔吉克斯坦",
	"Ethiopia": "埃塞俄比亚", "Tanzania": "坦桑尼亚", "Uganda": "乌干达", "Ghana": "加纳",
	"Morocco": "摩洛哥", "Algeria": "阿尔及利亚", "Tunisia": "突尼斯", "Libya": "利比亚",
	"Sudan": "苏丹", "Yemen": "也门", "Syria": "叙利亚", "Panama": "巴拿马", "Cuba": "古巴",
	"Ecuador": "厄瓜多尔", "Bolivia": "玻利维亚", "Paraguay": "巴拉圭", "Uruguay": "乌拉圭",
	"Costa Rica": "哥斯达黎加", "Guatemala": "危地马拉", "Honduras": "洪都拉斯",
	"El Salvador": "萨尔瓦多", "Nicaragua": "尼加拉瓜", "Dominican Republic": "多米尼加",
	"Jamaica": "牙买加", "Iceland": "冰岛", "Malta": "马耳他", "Cyprus": "塞浦路斯",
	"Brunei": "文莱", "Fiji": "斐济", "Papua New Guinea": "巴布亚新几内亚",
	"Mauritius": "毛里求斯", "Zimbabwe": "津巴布韦", "Zambia": "赞比亚",
	"Mozambique": "莫桑比克", "Angola": "安哥拉", "Cameroon": "喀麦隆",
	"Senegal": "塞内加尔", "Madagascar": "马达加斯加", "Somalia": "索马里",
	"New Caledonia": "新喀里多尼亚", "Puerto Rico": "波多黎各", "Greenland": "格陵兰",
	"Antarctica": "南极洲", "Gambia": "冈比亚", "Botswana": "博茨瓦纳",
	"Namibia": "纳米比亚", "Rwanda": "卢旺达", "Malawi": "马拉维", "Moldova": "摩尔多瓦",
	"Albania": "阿尔巴尼亚", "Bosnia and Herzegovina": "波斯尼亚和黑塞哥维那",
	"North Macedonia": "北马其顿", "Montenegro": "黑山", "Seychelles": "塞舌尔",
	"Fiji Islands": "斐济", "Togo": "多哥", "Benin": "贝宁", "Mali": "马里",
	"Niger": "尼日尔", "Burkina Faso": "布基纳法索", "Chad": "乍得",
	"Cote d'Ivoire": "科特迪瓦", "Guinea": "几内亚", "Liberia": "利比里亚",
	"Sierra Leone": "塞拉利昂", "Mauritania": "毛里塔尼亚", "Gabon": "加蓬",
	"Congo": "刚果（布）", "DR Congo": "刚果（金）",
	"South Sudan": "南苏丹", "Eritrea": "厄立特里亚", "Djibouti": "吉布提",
	"Comoros": "科摩罗", "Swaziland": "斯威士兰", "Lesotho": "莱索托",
	"Timor-Leste": "东帝汶", "Palestine": "巴勒斯坦", "Vanuatu": "瓦努阿图",
	"Samoa": "萨摩亚", "Tonga": "汤加", "Solomon Islands": "所罗门群岛",
	"Falkland Islands": "福克兰群岛", "Saint Helena": "圣赫勒拿",
	"French Polynesia": "法属波利尼西亚", "American Samoa": "美属萨摩亚",
}

// countryCN 将国家名规范为中文（世界地图 GeoJSON 的 name）。
func countryCN(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "0" {
		return ""
	}
	if cn, ok := countryMap[name]; ok {
		return cn
	}
	return name
}

// provinceSuffixes 省份/直辖市/自治区行政后缀（长后缀在前）。
var provinceSuffixes = []string{
	"壮族自治区", "维吾尔自治区", "回族自治区", "特别行政区", "自治区", "省", "市",
}

// normProvince 将省份规范为中文简称（对齐中国地图 GeoJSON 的 name）。
func normProvince(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return ""
	}
	for _, suf := range provinceSuffixes {
		if strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}
