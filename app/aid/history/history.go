package history

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"sync"

	"gopkg.in/mgo.v2/bson"

	"github.com/henrylee2cn/pholcus/app/downloader/request"
	"github.com/henrylee2cn/pholcus/common/mgo"
	"github.com/henrylee2cn/pholcus/common/mysql"
	"github.com/henrylee2cn/pholcus/common/pool"
	"github.com/henrylee2cn/pholcus/config"
	"github.com/henrylee2cn/pholcus/logs"
)

type (
	Historier interface {
		// 读取成功记录
		ReadSuccess(provider string, inherit bool)
		// 更新或加入成功记录
		UpsertSuccess(Record) bool
		// 删除成功记录
		DeleteSuccess(Record)
		// I/O输出成功记录，但不清缓存
		FlushSuccess(provider string)

		// 读取失败记录
		ReadFailure(provider string, inherit bool)
		// 更新或加入失败记录
		UpsertFailure(*request.Request) bool
		// 删除失败记录
		DeleteFailure(*request.Request)
		// I/O输出失败记录，但不清缓存
		FlushFailure(provider string)
		// 获取指定蜘蛛在上一次运行时失败的请求
		PullFailure(spiderName string) []*request.Request

		// 清空缓存，但不输出
		Empty()
	}
	Record interface {
		GetUrl() string
		GetMethod() string
	}
	History struct {
		*Success
		*Failure
		provider string
		sync.RWMutex
	}
)

var (
	MGO_DB = config.MGO.DB

	SUCCESS_FILE = config.HISTORY.FILE_NAME_PREFIX + "_y"
	FAILURE_FILE = config.HISTORY.FILE_NAME_PREFIX + "_n"

	SUCCESS_FILE_FULL = path.Join(config.HISTORY.DIR, SUCCESS_FILE)
	FAILURE_FILE_FULL = path.Join(config.HISTORY.DIR, FAILURE_FILE)
)

func New() Historier {
	return &History{
		Success: &Success{
			new: make(map[string]bool),
			old: make(map[string]bool),
		},
		Failure: &Failure{
			list: make(map[string]map[string]bool),
		},
	}
}

// 读取成功记录
func (self *History) ReadSuccess(provider string, inherit bool) {
	self.RWMutex.Lock()
	self.provider = provider
	self.RWMutex.Unlock()

	if !inherit {
		// 不继承历史记录时
		self.Success.old = make(map[string]bool)
		self.Success.new = make(map[string]bool)
		self.Success.inheritable = false
		return

	} else if self.Success.inheritable {
		// 本次与上次均继承历史记录时
		return

	} else {
		// 上次没有继承历史记录，但本次继承时
		self.Success.old = make(map[string]bool)
		self.Success.new = make(map[string]bool)
		self.Success.inheritable = true
	}

	switch provider {
	case "mgo":
		var docs = map[string]interface{}{}
		err := mgo.Mgo(&docs, "find", map[string]interface{}{
			"Database":   MGO_DB,
			"Collection": SUCCESS_FILE,
		})
		if err != nil {
			logs.Log.Error(" *     Fail  [读取成功记录][mgo]: %v\n", err)
			return
		}
		for _, v := range docs["Docs"].([]interface{}) {
			self.Success.old[v.(bson.M)["_id"].(string)] = true
		}

	case "mysql":
		db, err := mysql.DB()
		if err != nil {
			logs.Log.Error(" *     Fail  [读取成功记录][mysql]: %v\n", err)
			return
		}
		rows, err := mysql.New(db).
			SetTableName("`" + SUCCESS_FILE + "`").
			SelectAll()
		if err != nil {
			return
		}

		for rows.Next() {
			var id string
			err = rows.Scan(&id)
			self.Success.old[id] = true
		}

	default:
		f, err := os.Open(SUCCESS_FILE_FULL)
		if err != nil {
			return
		}
		defer f.Close()
		b, _ := ioutil.ReadAll(f)
		b[0] = '{'
		json.Unmarshal(append(b, '}'), &self.Success.old)
	}
	logs.Log.Informational(" *     [读取成功记录]: %v 条\n", len(self.Success.old))
}

// 读取失败记录
func (self *History) ReadFailure(provider string, inherit bool) {
	self.RWMutex.Lock()
	self.provider = provider
	self.RWMutex.Unlock()

	if !inherit {
		// 不继承历史记录时
		self.Failure.list = make(map[string]map[string]bool)
		self.Failure.inheritable = false
		return

	} else if self.Failure.inheritable {
		// 本次与上次均继承历史记录时
		return

	} else {
		// 上次没有继承历史记录，但本次继承时
		self.Failure.list = make(map[string]map[string]bool)
		self.Failure.inheritable = true
	}
	var fLen int
	switch provider {
	case "mgo":
		if mgo.Error() != nil {
			logs.Log.Error(" *     Fail  [读取失败记录][mgo]: %v\n", mgo.Error())
			return
		}

		var docs = []interface{}{}
		mgo.Call(func(src pool.Src) error {
			c := src.(*mgo.MgoSrc).DB(MGO_DB).C(FAILURE_FILE)
			return c.Find(nil).All(&docs)
		})

		for _, v := range docs {
			failure := v.(bson.M)["_id"].(string)
			req, err := request.UnSerialize(failure)
			if err != nil {
				continue
			}
			spName := req.GetSpiderName()
			if _, ok := self.Failure.list[spName]; !ok {
				self.Failure.list[spName] = make(map[string]bool)
			}
			self.Failure.list[spName][failure] = true
			fLen++
		}

	case "mysql":
		db, err := mysql.DB()
		if err != nil {
			logs.Log.Error(" *     Fail  [读取失败记录][mysql]: %v\n", err)
			return
		}
		rows, err := mysql.New(db).
			SetTableName("`" + FAILURE_FILE + "`").
			SelectAll()
		if err != nil {
			// logs.Log.Error("读取Mysql数据库中成功记录失败：%v", err)
			return
		}

		for rows.Next() {
			var id int
			var failure string
			err = rows.Scan(&id, &failure)
			req, err := request.UnSerialize(failure)
			if err != nil {
				continue
			}
			spName := req.GetSpiderName()
			if _, ok := self.Failure.list[spName]; !ok {
				self.Failure.list[spName] = make(map[string]bool)
			}
			self.Failure.list[spName][failure] = true
			fLen++
		}

	default:
		f, err := os.Open(FAILURE_FILE_FULL)
		if err != nil {
			return
		}
		b, _ := ioutil.ReadAll(f)
		f.Close()

		b[0] = '{'
		json.Unmarshal(
			append(b, '}'),
			&self.Failure.list,
		)
		for _, v := range self.Failure.list {
			fLen += len(v)
		}

	}
	logs.Log.Informational(" *     [读取失败记录]: %v 条\n", fLen)
}

// 清空缓存，但不输出
func (self *History) Empty() {
	self.RWMutex.Lock()
	self.Success.new = make(map[string]bool)
	self.Success.old = make(map[string]bool)
	self.Failure.list = make(map[string]map[string]bool)
	self.RWMutex.Unlock()
}

// I/O输出成功记录，但不清缓存
func (self *History) FlushSuccess(provider string) {
	self.RWMutex.Lock()
	self.provider = provider
	self.RWMutex.Unlock()
	sucLen, err := self.Success.flush(provider)
	logs.Log.Informational(" * ")
	if err != nil {
		logs.Log.Error("%v", err)
	} else {
		logs.Log.Informational(" *     [添加成功记录]: %v 条\n", sucLen)
	}
}

// I/O输出失败记录，但不清缓存
func (self *History) FlushFailure(provider string) {
	self.RWMutex.Lock()
	self.provider = provider
	self.RWMutex.Unlock()
	failLen, err := self.Failure.flush(provider)
	logs.Log.Informational(" * ")
	if err != nil {
		logs.Log.Error("%v", err)
	} else {
		logs.Log.Informational(" *     [添加失败记录]: %v 条\n", failLen)
	}
}
