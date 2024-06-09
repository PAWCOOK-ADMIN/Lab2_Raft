package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
type ApplyMsg struct {
	CommandValid bool        // 表示该 ApplyMsg 是否包含一个新提交的日志条目。
	Command      interface{} // 需要应用的命令，可以是任意类型。
	CommandIndex int         // 该命令在日志中的索引。

	// 下面字段用于 2D 部分：
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

// 日志压缩请求
type InstallSnapshotArgs struct {
	Term             int    // 发送请求方的任期
	LeaderId         int    // 请求方的LeaderId
	LastIncludeIndex int    // 快照最后applied的日志下标
	LastIncludeTerm  int    // 快照最后applied时的当前任期
	Data             []byte // 快照区块的原始字节流数据
	//Done bool
}

// 日志压缩响应
type InstallSnapshotReply struct {
	Term int
}

type RaftState string

const (
	Follower  RaftState = "Follower"
	Candidate           = "Candidate"
	Leader              = "Leader"
)

// Raft 集群中的一个节点，即一个状态机
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // 其他Raft节点上用于接收数据的端点。即（0，1）、（0，2）、（0，3）
	persister *Persister          // 用于保存节点持久化信息的对象
	me        int                 // 当前 Raft 节点的下标
	dead      int32               // 由 Kill() 函数设置，崩溃则值为1

	state         RaftState // 节点的状态，leader、Follower、Candidate
	appendEntryCh chan *Entry
	heartBeat     time.Duration // 心跳间隔
	electionTime  time.Time     // 应该进行leader选举的时间

	// 所有节点都应该有的持久化状态
	currentTerm int
	votedFor    int
	log         Log // 节点上的指令日志，严格按照顺序执行，则所有状态机都能达成一致

	// 所有服务器上都有的状态
	commitIndex int // 已知被提交的最高日志条目的索引，初始值为0，并且单调递增。
	lastApplied int // 已应用到状态机的最高日志条目的索引，初始值为0，并且单调递增。

	// leader 才有的状态
	nextIndex  []int //对于每一个服务器，要发送给该服务器的下一个日志条目的索引，初始值为领导者的最后一个日志索引加 1。
	matchIndex []int // 对于每一个服务器，已知复制到该服务器的最高日志条目的索引，初始值为 0，并且单调递增。

	applyCh   chan ApplyMsg
	applyCond *sync.Cond // 条件变量

	// lab2D 中用于传入快照点
	lastIncludeIndex int
	lastIncludeTerm  int

	// paper外自己追加的
	voteNum    int // 记录当前投票给了谁
	votedTimer time.Time
}

// persist 将 Raft 的持久状态保存到稳定存储中，以便在崩溃和重启后使用。
func (rf *Raft) persist() {
	DPrintVerbose("[%v]: STATE: %v", rf.me, rf.log.String()) // 打印当前节点的状态信息，主要用于调试和日志记录
	w := new(bytes.Buffer)                                   // 创建一个字节缓冲区，用于临时存储编码后的数据
	e := labgob.NewEncoder(w)                                // 创建一个新的编码器，将数据写入字节缓冲区

	e.Encode(rf.currentTerm) // 将 currentTerm 编码并写入缓冲区
	e.Encode(rf.votedFor)
	e.Encode(rf.log)
	e.Encode(rf.lastIncludeIndex)
	e.Encode(rf.lastIncludeTerm)

	data := w.Bytes()                // 获取缓冲区中的字节数组，即编码后的数据
	rf.persister.SaveRaftState(data) // 将编码后的数据保存到稳定存储中，以便在崩溃和重启后可以恢复
}

// readPersist 从现在保存的持久化状态中恢复.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}

	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm int
	var votedFor int
	var logs Log
	var lastIncludeIndex int
	var lastIncludeTerm int

	if d.Decode(&currentTerm) != nil || d.Decode(&votedFor) != nil || d.Decode(&logs) != nil || d.Decode(&lastIncludeIndex) != nil || d.Decode(&lastIncludeTerm) != nil {
		log.Fatal("failed to read persist\n")
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = logs
		rf.lastIncludeIndex = lastIncludeIndex
		rf.lastIncludeTerm = lastIncludeTerm
	}
}

// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {

	// Your code here (2D).

	return true
}

// Start
// 作用：接受客户端的command，并且应用在 raft 集群中（复制和提交）
// 返回值：成功提交日志的 index、当前节点的 term、当前节点是否是 leader
// 备注：
// - 1、只有leader才可以接收客户端命令。
// - 2、不能保证这个命令会被提交到 Raft 日志中，因为领导者可能会失败或失去选举。
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != Leader { // 如果不是leader，则结束
		return -1, rf.currentTerm, false
	}
	index := rf.getLastIndex() + 1 // 指令日志索引号+1
	term := rf.currentTerm

	// 新增一条日志保存到状态机中
	log := Entry{
		Command: command,
		Index:   index,
		Term:    term,
	}
	rf.log.append(log)

	// 持久化
	rf.persist()
	DPrintf("[%v]: term %v Start %v", rf.me, term, log)

	// 发送追加条目RPC
	rf.appendEntries(false)

	return index, term, true
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

// ticker 以心跳为周期不断检查自己的状态，如果没有收到心跳包，则开始一个新的leader选举过程
func (rf *Raft) ticker() {
	// 如果节点没有崩溃，则一直执行以下循环
	for rf.killed() == false {

		// 睡眠一个心跳的时间
		time.Sleep(rf.heartBeat)
		rf.mu.Lock()

		// 如果是leader，醒来后发送一个心跳包（追加条目RPC）
		if rf.state == Leader {
			rf.appendEntries(true)
		}

		// 如果醒来后，发现此刻在应该进行选举的超时时间之后，则说明没有收到心跳包，开启新的一轮leader选举
		if time.Now().After(rf.electionTime) {
			rf.leaderElection()
		}
		rf.mu.Unlock()
	}
}

// Make 服务或测试者希望创建一个 Raft 服务器。
// 所有 Raft 服务器的端口（包括这个服务器的端口）都在 peers[] 数组中。
// 这个服务器的端口是 peers[me]。
// 所有服务器的 peers[] 数组的顺序都是相同的。
// persister 是一个用于保存这个服务器的持久状态的地方，并且最初还包含最近保存的状态（如果有的话）。
// applyCh 是一个通道，测试者或服务期望 Raft 在这个通道上发送 ApplyMsg 消息。
// Make 函数必须快速返回，因此应该为任何长时间运行的工作启动 goroutines。
// 创建一个 Raft 集群的节点
func Make(peers []*labrpc.ClientEnd, me int, persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (2A, 2B, 2C).
	rf.state = Follower
	rf.currentTerm = 0 // 初始化任期为 0
	rf.votedFor = -1
	rf.heartBeat = 20 * time.Millisecond // 50毫秒
	rf.resetElectionTimer()

	rf.log = makeEmptyLog()        // 初始化保存日志的结构
	rf.log.append(Entry{-1, 0, 0}) // 增加一条空日志
	rf.commitIndex = 0
	rf.lastApplied = 0

	rf.lastIncludeIndex = 0
	rf.lastIncludeTerm = 0

	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))

	rf.applyCh = applyCh
	rf.applyCond = sync.NewCond(&rf.mu)

	// 从磁盘中读取持久化时写入的数据
	rf.readPersist(persister.ReadRaftState())

	// 同步快照信息
	if rf.lastIncludeIndex > 0 {
		rf.lastApplied = rf.lastIncludeIndex
	}

	// 启动一个定时器，开始leader选举
	go rf.ticker()

	go rf.applier()
	return rf
}

func (rf *Raft) apply() {
	rf.applyCond.Broadcast()
	DPrintf("[%v]: rf.applyCond.Broadcast()", rf.me)
}

// 日志提交
func (rf *Raft) applier() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 如果节点没有崩溃，则一直执行以下循环
	for !rf.killed() {
		// all server rule 1
		if rf.commitIndex > rf.lastApplied && rf.log.lastLog().Index > rf.lastApplied {
			rf.lastApplied++
			applyMsg := ApplyMsg{
				CommandValid:  true,
				SnapshotValid: false,
				Command:       rf.log.at(rf.restoreLogIndex(rf.lastApplied)).Command,
				CommandIndex:  rf.lastApplied,
			}

			// DPrintVerbose("[%v]: COMMIT %d: %v", rf.me, rf.lastApplied, rf.commits())

			rf.mu.Unlock()
			rf.applyCh <- applyMsg
			rf.mu.Lock()
		} else {
			rf.applyCond.Wait()
			DPrintf("[%v]: rf.applyCond.Wait()", rf.me)
		}
	}
}

func (rf *Raft) commits() string {

	nums := []string{}
	var i int
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(fmt.Sprintf("len(log): %v", rf.log.len()))
			fmt.Println(fmt.Sprintf("lastIncludeIndex: %v", rf.lastIncludeIndex))
			fmt.Println(fmt.Sprintf("i: %v", i))
		}
	}()
	for i = 0; i <= rf.lastApplied; i++ {
		nums = append(nums, fmt.Sprintf("%4d", rf.log.at(rf.restoreLogIndex(i)).Command))
	}
	return fmt.Sprint(strings.Join(nums, "|"))
}

func (rf *Raft) leaderSendSnapShot(server int) {

	rf.mu.Lock()

	args := InstallSnapshotArgs{
		Term:             rf.currentTerm,
		LeaderId:         rf.me,
		LastIncludeIndex: rf.lastIncludeIndex,
		LastIncludeTerm:  rf.lastIncludeTerm,
		Data:             rf.persister.ReadSnapshot(), // leader 节点的快照信息
	}
	reply := InstallSnapshotReply{}

	rf.mu.Unlock()

	// 给 follower 发送快照
	resp := rf.sendSnapShot(server, &args, &reply)

	// 发送成功
	if resp == true {
		rf.mu.Lock()
		// leader 身份变更
		if rf.state != Leader || rf.currentTerm != args.Term {
			rf.mu.Unlock()
			return
		}

		// 如果返回的 term 比自己大说明自身数据已经不合适了
		if reply.Term > rf.currentTerm {
			rf.state = Follower
			rf.votedFor = -1
			rf.voteNum = 0
			rf.persist()
			rf.votedTimer = time.Now()
			rf.mu.Unlock()
			return
		}

		rf.matchIndex[server] = args.LastIncludeIndex
		rf.nextIndex[server] = args.LastIncludeIndex + 1

		rf.mu.Unlock()
		return
	}
}

// 发送快照信息给 follower
func (rf *Raft) sendSnapShot(server int, args *InstallSnapshotArgs, reply *InstallSnapshotReply) bool {
	ok := rf.peers[server].Call("Raft.InstallSnapShot", args, reply)
	return ok
}

// InstallSnapShot follower 收到快照时进行安装
func (rf *Raft) InstallSnapShot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()
	// 如果当前节点的任期更新
	if rf.currentTerm > args.Term {
		reply.Term = rf.currentTerm
		rf.mu.Unlock()
		return
	}

	rf.currentTerm = args.Term
	reply.Term = args.Term

	rf.state = Follower
	rf.votedFor = -1
	rf.voteNum = 0
	rf.persist()
	rf.votedTimer = time.Now()

	// 如果当前节点的快照替换的日志更多
	if rf.lastIncludeIndex >= args.LastIncludeIndex {
		rf.mu.Unlock()
		return
	}

	// 将快照后的logs切割，快照前的直接applied
	index := args.LastIncludeIndex
	var tempLog Log
	tempLog.Entries = make([]Entry, 0)
	tempLog.Entries = append(tempLog.Entries, Entry{})

	for i := index + 1; i <= rf.getLastIndex(); i++ {
		tempLog.Entries = append(tempLog.Entries, *rf.restoreLog(i))
	}

	rf.lastIncludeTerm = args.LastIncludeTerm
	rf.lastIncludeIndex = args.LastIncludeIndex

	// 更新提交信息
	rf.log = tempLog
	if index > rf.commitIndex {
		rf.commitIndex = index
	}
	if index > rf.lastApplied {
		rf.lastApplied = index
	}

	// 持久化
	rf.persister.SaveStateAndSnapshot(rf.persistData(), args.Data)

	msg := ApplyMsg{
		SnapshotValid: true,
		Snapshot:      args.Data,
		SnapshotTerm:  rf.lastIncludeTerm,
		SnapshotIndex: rf.lastIncludeIndex,
	}
	rf.mu.Unlock()

	rf.applyCh <- msg

}

// 通过快照偏移还原真实日志条目
func (rf *Raft) restoreLog(curIndex int) *Entry {
	return rf.log.at(curIndex - rf.lastIncludeIndex)
}

// save Raft's persistent status to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
func (rf *Raft) persistData() []byte {
	// Your code here (2C).
	// Example:
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.log)
	e.Encode(rf.lastIncludeIndex)
	e.Encode(rf.lastIncludeTerm)
	data := w.Bytes()

	return data
}

// 获取需要发送给节点的 prelog，通过快照偏移还原真实 PrevLogInfo
func (rf *Raft) getPrevLogInfo(server int) (int, int) {
	preIndex := rf.nextIndex[server] - 1
	// 忽略-------------为了同步初始化时加入的一个nil log的下标
	lastIndex := rf.getLastIndex()
	if preIndex > lastIndex {
		preIndex = lastIndex
	}
	// 忽略-------------
	return preIndex, rf.restoreLogTerm(preIndex)
}

// 逻辑index to 实际日志文件的位置
func (rf *Raft) restoreLogIndex(curIndex int) int {
	return curIndex - rf.lastIncludeIndex
}

// 通过快照偏移还原真实日志任期
func (rf *Raft) restoreLogTerm(curIndex int) int {
	// 如果当前index与快照一致/日志为空，直接返回快照/快照初始化信息，否则根据快照计算
	if curIndex-rf.lastIncludeIndex == 0 {
		return rf.lastIncludeTerm
	}
	//fmt.Printf("[GET] curIndex:%v,rf.lastIncludeIndex:%v\n", curIndex, rf.lastIncludeIndex)
	return rf.log.at(curIndex - rf.lastIncludeIndex).Term
}

// 获取最后的快照日志下标(代表已存储）
func (rf *Raft) getLastIndex() int {
	return rf.log.len() - 1 + rf.lastIncludeIndex
}

// index代表是快照apply应用的index,而snapshot代表的是上层service传来的快照字节流，包括了Index之前的数据
// 这个函数的目的是把安装到快照里的日志抛弃，并安装快照数据，同时更新快照下标，属于peers自身主动更新，与leader发送快照不冲突
// 将到 index 为止的日志进行快照
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).
	if rf.killed() {
		return
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()

	//fmt.Println("[Snapshot] commintIndex", rf.commitIndex)
	// 如果下标大于自身的提交，说明没被提交不能安装快照，如果自身快照点大于index说明不需要安装
	if rf.lastIncludeIndex >= index || index > rf.commitIndex {
		return
	}

	// 更新快照日志
	var tempLog Log
	tempLog.Entries = make([]Entry, 0)
	tempLog.Entries = append(tempLog.Entries, Entry{})

	for i := index + 1; i <= rf.getLastIndex(); i++ {
		tempLog.Entries = append(tempLog.Entries, *rf.restoreLog(i))
	}

	//fmt.Printf("[Snapshot-Rf(%v)]rf.commitIndex:%v,index:%v\n", rf.me, rf.commitIndex, index)
	// 更新快照下标/任期
	if index == rf.getLastIndex()+1 {
		rf.lastIncludeTerm = rf.getLastTerm()
	} else {
		rf.lastIncludeTerm = rf.restoreLogTerm(index)
	}

	rf.lastIncludeIndex = index
	rf.log = tempLog

	// apply了快照就应该重置commitIndex、lastApplied
	if index > rf.commitIndex {
		rf.commitIndex = index
	}
	if index > rf.lastApplied {
		rf.lastApplied = index
	}

	// 持久化快照信息
	rf.persister.SaveStateAndSnapshot(rf.persistData(), snapshot)
}

// 获取最后的任期(快照版本
func (rf *Raft) getLastTerm() int {
	// 因为初始有填充一个，否则最直接len == 0
	if rf.log.len()-1 == 0 {
		return rf.lastIncludeTerm
	} else {
		return rf.log.at(rf.log.len() - 1).Term
	}
}
