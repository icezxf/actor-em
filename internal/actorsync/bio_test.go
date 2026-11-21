package actorsync

import "testing"

// 这些文本全部来自 936 条真实数据的 bio 字段，不是编的。
// 每一条都对应一次真实踩坑。
func TestValidBio(t *testing.T) {
	keep := []struct{ name, bio string }{
		{"星野景子", "ほしの景子（星野景子,Hoshino Keiko）是一位日本AV女优，1988年4月11日出生。 她于27岁时以E-BODY专属出道。"},
		{"早野诗", "はやのうた（早野诗）是一位日本AV女优，出生于2000年9月7日。她曾隶属于Bstar经纪公司。2021年3月，她以S1专属出道。"},
		{"P酱", "P醬（日語：ピーちゃん，英語：P-Chan，1988年2月10日—），日本寫真偶像，前AV女優。"},
		{"あおいれな", "葵玲奈（日語：あおい れな，1996年3月19日—），日本前AV女優和歌手，2021年從AV界引退、成為AV監製。"},
		{"Mio Fukada", "深田みお(Fukada mio)，153cm出生于1998-12-27，她是一名日本女优。"},
		// 艺名变更：条目叫「志田明里」，简介讲的是「あいだ飛鳥」——是同一人，
		// aliases 里两个名字都有。不能因为标题对不上就丢。
		{"志田明里", "あいだ飛鳥（あいだ あすか，1985年11月30日—2019年3月31日），日本前性感女优，所属ジールグループ。"},
		// 汉字/假名写法差异：橘日菜 = 立花日菜（都是 tachibana）
		{"橘日菜", "立花日菜（たちばなひな）是一位日本的AV女优。 【立花日菜的特点】立花日菜是一位超敏感的女性，她拥有纤细白皙的身体。"},
	}
	for _, c := range keep {
		if !ValidBio(c.bio) {
			t.Errorf("%s 的真简介被误杀：%s", c.name, c.bio[:40])
		}
	}

	drop := []struct{ name, bio, why string }{
		{"もも", "桃（學名：Prunus persica）是源自中国，蔷薇科李属的一种植物，花可以观赏，果实多汁，作为水果，可以生食或製桃脯、罐头等，核仁也可以食用。", "维基植物条目"},
		{"DA", "Da或DA可能是指以下事物：\n\ndeviantART的常用缩写，这是一个艺术导向的社交网络服务网站。", "消歧义页"},
		{"David", "大衛王是公元前10世紀以色列聯合王國的第二任國王。\n大衛还可以指：\n\n大衛，西方常见人名", "上古人物 + 消歧义"},
		{"JULIA", "Julia或JULIA可能是指：\n\nJulia語言，一种用于科学计算的语言\n華裔澳籍女歌手吳卓源的英文名", "消歧义页（即便列了 AV 女优）"},
		{"あいり", "日語讀作あいり的知名人物可以指：\n\n寫成あいり\n三田愛理，日本演員及藝人\n日向愛理，日本AV女優", "消歧义页（即便含 AV 关键词）"},
		{"Michael", "迈克尔（英語：Michael）是源于希伯来语的名字，同時也是基督徒使用的聖名之一。", "人名条目"},
		{"つよぽん", "草彅（jiǎn）剛（1974年7月9日—），是日本男歌手、男演員，已解散的日本偶像組合SMAP的前成員。身高170cm。", "同名男艺人"},
		{"そらる", "SORARU（日語：そらる）（1988年11月3日—），是日本男性歌手。出生於宮城縣。身高176公分。", "同名男歌手"},
		{"イェスル", "也速儿（13世纪—1260年之后），弘吉剌氏，元宪宗蒙哥的王妃，贞节皇后忽都台的妹妹。", "13 世纪王妃"},
		{"麻美润", "身高: 167 | 罩杯: E | 出身: 東京都 | 生涯: 2021年- | 出道: ヤリたくなったら、即マン!", "MDCx 属性串"},
		{"一之濑恋", "身高: 160 | 罩杯: C | 出身: 神奈川県 | 生涯: 2015年 - | 标签: 美少女", "MDCx 属性串"},
		{"", "短文本", "太短"},
	}
	for _, c := range drop {
		if ValidBio(c.bio) {
			t.Errorf("%s（%s）应被丢弃，却通过了", c.name, c.why)
		}
	}
}

func TestBioConflictsHeight(t *testing.T) {
	// 真实案例：简介是另一个「深田みお」的，身高差 8cm
	bio := "深田みお(Fukada mio)，161cm出生于1995-11-12，她是一名日本女优。"
	if !BioConflictsHeight(bio, 153) {
		t.Error("161cm 的简介挂到 153cm 的人身上，应判为冲突")
	}
	if BioConflictsHeight(bio, 161) {
		t.Error("身高一致时不该判冲突")
	}
	// 3cm 以内是各源四舍五入的正常差异
	if BioConflictsHeight(bio, 158) {
		t.Error("差 3cm 在容差内")
	}
	if BioConflictsHeight(bio, 0) {
		t.Error("结构化字段没身高时无从比较，不该判冲突")
	}
	// 没写身高的简介不该被判冲突
	if BioConflictsHeight("她是一名日本AV女优，出生于东京都。", 153) {
		t.Error("简介没提身高时不该判冲突")
	}
}

func TestBioHeightCm(t *testing.T) {
	cases := []struct {
		bio  string
		want int
	}{
		{"深田みお(Fukada mio)，161cm出生于1995-11-12", 161},
		{"身高170cm。", 170},
		{"身高 158 公分", 158},
		{"她是一名日本AV女优", 0},
		{"体重 45kg", 0},
		// 超出人类身高的数字不该被当成身高
		{"出道作编号 999cm 是乱的", 0},
	}
	for _, c := range cases {
		if got := BioHeightCm(c.bio); got != c.want {
			t.Errorf("BioHeightCm(%q) = %d, 期望 %d", c.bio, got, c.want)
		}
	}
}

// 简介只收中文：英文条目要在入库前拦掉，日文条目同样不收。
func TestLooksChineseBio(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"中文正文", "三上悠亚，原名鬼头桃菜（きとう ももな），1993年8月16日出生于日本爱知县名古屋市，日本女优、歌手，2015年出道。", true},
		{"纯英文", "AIKA is a Japanese AV actress active since 2007. She has appeared in many videos and is known for her work.", false},
		{"日文为主", "あいかは日本のAV女優である。二〇〇七年にデビューして、多くの作品に出演している。趣味は料理と旅行です。", false},
		{"字段串不是简介", "身高: 155cm 罩杯: D 三围: 84/60/86", false},
	}
	for _, tc := range cases {
		if got := LooksChineseBio(tc.in); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
