// 数据收集
package collector

import (
	"runtime"
	"time"

	"github.com/henrylee2cn/pholcus/app/spider"
	"github.com/henrylee2cn/pholcus/config"
	"github.com/henrylee2cn/pholcus/runtime/cache"
)

// 每个爬取任务的数据容器
type Collector struct {
	*spider.Spider
	*DockerQueue
	DataChan chan DataCell
	FileChan chan FileCell
	ctrl     chan bool //长度为零时退出并输出
	timing   time.Time //上次输出完成的时间点
	outType  string    //输出方式
	sum      [3]uint64 //收集的数据总数[文本过去，文本现在，文件],非并发安全
	outCount [4]uint   //[文本输出开始，文本输出结束，文件输出开始，文件输出结束]
}

func NewCollector() *Collector {
	self := &Collector{
		DataChan:    make(chan DataCell, config.DATA_CAP),
		FileChan:    make(chan FileCell, 512),
		DockerQueue: NewDockerQueue(),
		ctrl:        make(chan bool, 1),
	}
	return self
}

func (self *Collector) Init(sp *spider.Spider) {
	self.Spider = sp
	self.outType = cache.Task.OutType
	self.DataChan = make(chan DataCell, config.DATA_CAP)
	self.FileChan = make(chan FileCell, 512)
	self.DockerQueue = NewDockerQueue()
	self.ctrl = make(chan bool, 1)
	self.sum = [3]uint64{}
	self.outCount = [4]uint{}
	self.timing = cache.StartTime
}

func (self *Collector) CollectData(dataCell DataCell) {
	self.DataChan <- dataCell
}

func (self *Collector) CollectFile(fileCell FileCell) {
	self.FileChan <- fileCell
}

func (self *Collector) CtrlW() {
	self.ctrl <- true
}

func (self *Collector) CtrlR() {
	<-self.ctrl
}

func (self *Collector) CtrlLen() int {
	return len(self.ctrl)
}

// 数据转储输出
func (self *Collector) Manage() {
	// 标记开始，令self.Ctrl长度不为零
	self.CtrlW()

	// 开启文件输出协程
	go self.SaveFile()

	// 只有当收到退出通知并且通道内无数据时，才退出循环
	for !(self.CtrlLen() == 0 && len(self.DataChan) == 0) {
		select {
		case data := <-self.DataChan:
			self.dockerOne(data)
		default:
			runtime.Gosched()
		}
	}

	// 将剩余收集到但未输出的数据输出
	self.goOutput(self.Curr)

	// 等待所有输出完成
	for (self.outCount[0] > self.outCount[1]) || (self.outCount[2] > self.outCount[3]) || len(self.FileChan) > 0 {
		runtime.Gosched()
	}

	// 返回报告
	self.Report()
}

func (self *Collector) dockerOne(data DataCell) {
	self.Dockers[self.Curr] = append(self.Dockers[self.Curr], data)

	if len(self.Dockers[self.Curr]) >= cache.Task.DockerCap {
		// curDocker存满后输出
		self.goOutput(self.Curr)
		// 更换一个空Docker用于curDocker
		self.Change()
	}
}

func (self *Collector) goOutput(dataIndex int) {
	self.outCount[0]++
	go func() {
		self.Output(dataIndex)
		self.outCount[1]++
	}()
}

// 获取文本数据总量
func (self *Collector) dataSum() uint64 {
	return self.sum[1]
}

// 更新文本数据总量
func (self *Collector) setDataSum(add uint64) {
	self.sum[0], self.sum[1] = self.sum[1], self.sum[1]+add
}

// 获取文件数据总量
func (self *Collector) fileSum() uint64 {
	return self.sum[2]
}

// 更新文件数据总量
func (self *Collector) setFileSum(add uint64) {
	self.sum[2] = self.sum[2] + add
}

// 返回报告
func (self *Collector) Report() {
	cache.ReportChan <- &cache.Report{
		SpiderName: self.Spider.GetName(),
		Keyword:    self.GetKeyword(),
		DataNum:    self.dataSum(),
		FileNum:    self.fileSum(),
		Time:       time.Since(cache.StartTime),
	}
}
