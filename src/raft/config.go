package raft

//
// support for Raft tester.
//
// we will use the original config.go to test your code for grading.
// so, while you can modify this code to help you debug, please
// test with the original before submitting.
//

import "6.824/labgob"
import "6.824/labrpc"
import "bytes"
import "log"
import "sync"
import "testing"
import "runtime"
import "math/rand"
import crand "crypto/rand"
import "math/big"
import "encoding/base64"
import "time"
import "fmt"

// 生成一个指定长度的随机字符串
func randstring(n int) string {
	b := make([]byte, 2*n)
	crand.Read(b) // 使用crypto/rand包生成随机字节并填充b
	s := base64.URLEncoding.EncodeToString(b)
	return s[0:n] // 返回前n个字符，作为最终的随机字符串
}

// 返回一个随机数，范围0~2^62
func makeSeed() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := crand.Int(crand.Reader, max)
	x := bigx.Int64()
	return x
}

type config struct {
	mu        sync.Mutex            // 互斥锁，用于保护并发访问
	t         *testing.T            // 测试对象，用于记录测试状态和报告错误
	net       *labrpc.Network       // 模拟的网络，用于连接各个 Raft 节点
	n         int                   // Raft节点的个数
	rafts     []*Raft               // Raft 节点的实例数组
	applyErr  []string              // 从应用通道读取错误信息
	connected []bool                // 每个服务器是否在线的标志数组
	saved     []*Persister          // 每个节点的持久化状态
	endnames  [][]string            // 所有端点的名字，比如 endnames[1][2]，表示节点1可以发数据到节点2，端点位于节点2，节点1->节点2的网络是通的
	logs      []map[int]interface{} // 每个服务器已提交日志的副本，下标对应Raft节点的下标
	start     time.Time             // make_config() 被调用的时间

	// 统计信息
	t0        time.Time // cfg.begin() 在 test_test.go 中被调用的时间
	rpcs0     int       // 测试开始时的 RPC 总数
	cmds0     int       // 测试开始时的共识数
	bytes0    int64     // 测试开始时传输的数据量
	maxIndex  int       // 当前测试中的最大日志索引
	maxIndex0 int       // 测试开始时的最大日志索引
}

var ncpu_once sync.Once

func make_config(t *testing.T, n int, unreliable bool, snapshot bool) *config {
	ncpu_once.Do(func() { // 执行匿名函数，并且并发场景下保证该函数只执行一次
		if runtime.NumCPU() < 2 {
			fmt.Printf("warning: only one CPU, which may conceal locking bugs\n")
		}
		rand.Seed(makeSeed()) // 设置 Go 标准库中的随机数生成器的种子，使得后续调用 math/rand 包中的随机数生成函数时可以生成基于该种子的伪随机数。
	})
	runtime.GOMAXPROCS(4) // 设置可以同时执行的操作系统线程的最大数量
	cfg := &config{}
	cfg.t = t
	cfg.net = labrpc.MakeNetwork()                // 创建一个网络对象
	cfg.n = n                                     // 设置服务器数量
	cfg.applyErr = make([]string, cfg.n)          // 初始化 applyErr 数组
	cfg.rafts = make([]*Raft, cfg.n)              // 初始化 Raft 实例数组
	cfg.connected = make([]bool, cfg.n)           // 初始化连接状态数组
	cfg.saved = make([]*Persister, cfg.n)         // 初始化持久化对象数组
	cfg.endnames = make([][]string, cfg.n)        // 初始化端点名称数组
	cfg.logs = make([]map[int]interface{}, cfg.n) // 初始化日志数组
	cfg.start = time.Now()                        // 记录当前时间

	// 设置网络是否可靠
	cfg.setunreliable(unreliable)

	// 设置长延迟模式
	cfg.net.LongDelays(true)

	// 选择 applier 函数，根据是否使用快照决定使用哪一个
	applier := cfg.applier
	if snapshot {
		applier = cfg.applierSnap
	}

	// 创建并启动 Raft 集群
	for i := 0; i < cfg.n; i++ {
		cfg.logs[i] = map[int]interface{}{}
		cfg.start1(i, applier) // 启动 Raft 节点
	}

	// 节点之间进行连接
	for i := 0; i < cfg.n; i++ {
		cfg.connect(i)
	}

	return cfg
}

// 关闭一个 Raft 服务器，但保存其持久化状态。
func (cfg *config) crash1(i int) {
	cfg.disconnect(i)       // 断开服务器的连接，即端点关闭
	cfg.net.DeleteServer(i) // 删除掉节点，即其上RPC方法变得不可调用

	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	// 创建一个新的持久化器，以防旧实例继续更新持久化器。
	// 但复制旧持久化器的内容，以便始终将最后的持久状态传递给 Make()。
	if cfg.saved[i] != nil {
		cfg.saved[i] = cfg.saved[i].Copy()
	}

	rf := cfg.rafts[i]
	if rf != nil {
		cfg.mu.Unlock()
		rf.Kill() // 关闭 Raft 服务器
		cfg.mu.Lock()
		cfg.rafts[i] = nil // 将对应的 Raft 实例设置为 nil
	}

	if cfg.saved[i] != nil {
		raftlog := cfg.saved[i].ReadRaftState()              // 读取 Raft 日志状态
		snapshot := cfg.saved[i].ReadSnapshot()              // 读取快照
		cfg.saved[i] = &Persister{}                          // 创建新的持久化器实例
		cfg.saved[i].SaveStateAndSnapshot(raftlog, snapshot) // 保存状态和快照
	}
}

func (cfg *config) checkLogs(i int, m ApplyMsg) (string, bool) {
	err_msg := ""
	v := m.Command
	for j := 0; j < len(cfg.logs); j++ {
		if old, oldok := cfg.logs[j][m.CommandIndex]; oldok && old != v {
			log.Printf("%v: log %v; server %v\n", i, cfg.logs[i], cfg.logs[j])
			// some server has already committed a different value for this entry!
			err_msg = fmt.Sprintf("commit index=%v server=%v %v != server=%v %v",
				m.CommandIndex, i, m.Command, j, old)
		}
	}
	_, prevok := cfg.logs[i][m.CommandIndex-1]
	cfg.logs[i][m.CommandIndex] = v
	if m.CommandIndex > cfg.maxIndex {
		cfg.maxIndex = m.CommandIndex
	}
	return err_msg, prevok
}

// applier 从 applyCh 读取消息，并检查它们是否与日志内容匹配
func (cfg *config) applier(i int, applyCh chan ApplyMsg) {
	for m := range applyCh {
		if m.CommandValid == false {
			// 忽略其他类型的 ApplyMsg
		} else {
			// 加锁以检查日志内容
			cfg.mu.Lock()
			err_msg, prevok := cfg.checkLogs(i, m)
			cfg.mu.Unlock()

			// 检查提交的命令索引是否按顺序
			if m.CommandIndex > 1 && prevok == false {
				err_msg = fmt.Sprintf("server %v apply out of order %v", i, m.CommandIndex) // 如果不是按顺序提交，生成错误信息
			}

			// 如果有错误信息，记录错误并继续读取
			if err_msg != "" {
				log.Fatalf("apply error: %v\n", err_msg)
				cfg.applyErr[i] = err_msg
				// 在出错后继续读取消息，以防止 Raft 阻塞并持有锁
			}
		}
	}
}

const SnapShotInterval = 10

// periodically snapshot raft state
func (cfg *config) applierSnap(i int, applyCh chan ApplyMsg) {
	lastApplied := 0
	for m := range applyCh {
		if m.SnapshotValid {
			//DPrintf("Installsnapshot %v %v\n", m.SnapshotIndex, lastApplied)
			cfg.mu.Lock()
			if cfg.rafts[i].CondInstallSnapshot(m.SnapshotTerm,
				m.SnapshotIndex, m.Snapshot) {
				cfg.logs[i] = make(map[int]interface{})
				r := bytes.NewBuffer(m.Snapshot)
				d := labgob.NewDecoder(r)
				var v int
				if d.Decode(&v) != nil {
					log.Fatalf("decode error\n")
				}
				cfg.logs[i][m.SnapshotIndex] = v
				lastApplied = m.SnapshotIndex
			}
			cfg.mu.Unlock()
		} else if m.CommandValid && m.CommandIndex > lastApplied {
			//DPrintf("apply %v lastApplied %v\n", m.CommandIndex, lastApplied)
			cfg.mu.Lock()
			err_msg, prevok := cfg.checkLogs(i, m)
			cfg.mu.Unlock()
			if m.CommandIndex > 1 && prevok == false {
				err_msg = fmt.Sprintf("server %v apply out of order %v", i, m.CommandIndex)
			}
			if err_msg != "" {
				log.Fatalf("apply error: %v\n", err_msg)
				cfg.applyErr[i] = err_msg
				// keep reading after error so that Raft doesn't block
				// holding locks...
			}
			lastApplied = m.CommandIndex
			if (m.CommandIndex+1)%SnapShotInterval == 0 {
				w := new(bytes.Buffer)
				e := labgob.NewEncoder(w)
				v := m.Command
				e.Encode(v)
				cfg.rafts[i].Snapshot(m.CommandIndex, w.Bytes())
			}
		} else {
			// Ignore other types of ApplyMsg or old
			// commands. Old command may never happen,
			// depending on the Raft implementation, but
			// just in case.
			// DPrintf("Ignore: Index %v lastApplied %v\n", m.CommandIndex, lastApplied)

		}
	}
}

// 启动或重新启动一个Raft实例。
// 如果实例已经存在，先"杀死"它。
// 分配新的外发端口文件名和一个新的状态持久化器，以隔离该服务器的前一个实例。
// 因为我们实际上不能真的杀死它。
func (cfg *config) start1(i int, applier func(int, chan ApplyMsg)) {
	cfg.crash1(i) // "杀死"现有的Raft实例

	// 创建一组新的外发ClientEnd名称
	// 这样旧的崩溃实例的ClientEnd不能发送消息。
	cfg.endnames[i] = make([]string, cfg.n)
	for j := 0; j < cfg.n; j++ {
		cfg.endnames[i][j] = randstring(20)
	}

	// 使用ClientEnd名称创建一组新的ClientEnds实例
	ends := make([]*labrpc.ClientEnd, cfg.n)
	for j := 0; j < cfg.n; j++ {
		ends[j] = cfg.net.MakeEnd(cfg.endnames[i][j]) // 为每个端点创建ClientEnd
		cfg.net.Connect(cfg.endnames[i][j], j)        // 将端点绑定到对应的服务器
	}

	cfg.mu.Lock()

	/// 创建一个新的持久化器，以防止旧实例覆盖新实例的持久状态。
	// 但复制旧持久化器的内容，以便我们始终将最后的持久状态传递给Make()。
	if cfg.saved[i] != nil {
		cfg.saved[i] = cfg.saved[i].Copy() // 复制旧的持久化器内容
	} else {
		cfg.saved[i] = MakePersister() // 创建一个新的持久化器
	}

	cfg.mu.Unlock()

	// 创建一个新的通道，用于接收应用消息
	applyCh := make(chan ApplyMsg)

	rf := Make(ends, i, cfg.saved[i], applyCh) // 创建一个新的Raft实例

	cfg.mu.Lock()
	cfg.rafts[i] = rf // 将新的Raft实例存储在配置中
	cfg.mu.Unlock()

	go applier(i, applyCh) // 启动一个新的goroutine来处理应用消息

	svc := labrpc.MakeService(rf) // 创建一个新的RPC服务
	srv := labrpc.MakeServer()    // 创建一个新的RPC服务器
	srv.AddService(svc)           // 将服务添加到服务器
	cfg.net.AddServer(i, srv)     // 将服务器添加到网络中
}

func (cfg *config) checkTimeout() {
	// enforce a two minute real-time limit on each test
	if !cfg.t.Failed() && time.Since(cfg.start) > 120*time.Second {
		cfg.t.Fatal("test took longer than 120 seconds")
	}
}

func (cfg *config) cleanup() {
	for i := 0; i < len(cfg.rafts); i++ {
		if cfg.rafts[i] != nil {
			cfg.rafts[i].Kill()
		}
	}
	cfg.net.Cleanup()
	cfg.checkTimeout()
}

// 将服务器i连接到网络
func (cfg *config) connect(i int) {
	// fmt.Printf("connect(%d)\n", i)

	// 标记服务器i为已连接
	cfg.connected[i] = true

	// 处理服务器i的外发端口
	for j := 0; j < cfg.n; j++ {
		if cfg.connected[j] { // 如果服务器j也已连接
			endname := cfg.endnames[i][j] // 获取服务器i到服务器j的连接端口名
			cfg.net.Enable(endname, true) // 启用该端点，使其能够进行通信
		}
	}

	// 处理服务器i的入站端口
	for j := 0; j < cfg.n; j++ {
		if cfg.connected[j] { // 如果服务器j也已连接
			endname := cfg.endnames[j][i] // 获取服务器j到服务器i的连接端口名
			cfg.net.Enable(endname, true) // 启用该端点，使其能够进行通信
		}
	}
}

// disconnect 断开服务器 i 与网络的连接。
func (cfg *config) disconnect(i int) {
	// fmt.Printf("disconnect(%d)\n", i)

	cfg.connected[i] = false // 节点i设置为离线

	// 处理服务器 i 的所有出站连接
	for j := 0; j < cfg.n; j++ {
		if cfg.endnames[i] != nil { // 以节点i为起点的端点
			endname := cfg.endnames[i][j]
			cfg.net.Enable(endname, false) // 关闭端点
		}
	}

	// 处理所有入站连接到服务器 i
	for j := 0; j < cfg.n; j++ {
		if cfg.endnames[j] != nil {
			endname := cfg.endnames[j][i]  // 以节点i为终点的端点
			cfg.net.Enable(endname, false) // 关闭端点
		}
	}
}

func (cfg *config) rpcCount(server int) int {
	return cfg.net.GetCount(server)
}

func (cfg *config) rpcTotal() int {
	return cfg.net.GetTotalCount()
}

func (cfg *config) setunreliable(unrel bool) {
	cfg.net.Reliable(!unrel)
}

func (cfg *config) bytesTotal() int64 {
	return cfg.net.GetTotalBytes()
}

func (cfg *config) setlongreordering(longrel bool) {
	cfg.net.LongReordering(longrel)
}

// checkOneLeader 检查是否只有一个 leader。
// 这个函数会尝试多次以防止在 leader 重选过程中出现问题。
func (cfg *config) checkOneLeader() int {
	// 尝试检查 leader 多次，10 次
	for iters := 0; iters < 10; iters++ {
		// 随机等待 450 到 550 毫秒之间的时间
		ms := 450 + (rand.Int63() % 100)
		time.Sleep(time.Duration(ms) * time.Millisecond)

		// 创建一个 map 来记录每个 term 对应的 leaders 列表
		leaders := make(map[int][]int)
		for i := 0; i < cfg.n; i++ {
			if cfg.connected[i] { // 仅检查已连接的节点（即在线）
				if term, leader := cfg.rafts[i].GetState(); leader { // 获取节点的当前状态，如果是 leader，将其加入到 leaders map 中
					leaders[term] = append(leaders[term], i)
				}
			}
		}

		// 变量用于追踪最后一个有 leader 的 term
		lastTermWithLeader := -1
		for term, leaders := range leaders {
			// 如果一个 term 中有多个 leader，记录错误
			if len(leaders) > 1 {
				cfg.t.Fatalf("term %d has %d (>1) leaders", term, len(leaders)) // Fatalf 会终止测试
			}
			// 更新最后一个有 leader 的 term
			if term > lastTermWithLeader {
				lastTermWithLeader = term
			}
		}

		// 如果找到任何 leader，返回最后一个有 leader 的 term 的 leader
		if len(leaders) != 0 {
			return leaders[lastTermWithLeader][0]
		}
	}

	// 如果没有找到 leader，记录错误并返回 -1
	cfg.t.Fatalf("expected one leader, got none")
	return -1
}

// checkTerms 检查所有节点的任期是否相同。
// 如果检测到不一致则立即失败。
func (cfg *config) checkTerms() int {
	term := -1
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			xterm, _ := cfg.rafts[i].GetState() // 获取节点的当前 term
			if term == -1 {                     // 如果 term 尚未设置，初始化为当前节点的 term
				term = xterm
			} else if term != xterm {
				cfg.t.Fatalf("servers disagree on term") // 如果已经设置了 term 且当前节点的 term 不一致，测试失败
			}
		}
	}
	return term // 所有节点的任期相同
}

// check that there's no leader
func (cfg *config) checkNoLeader() {
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			_, is_leader := cfg.rafts[i].GetState()
			if is_leader {
				cfg.t.Fatalf("expected no leader, but %v claims to be leader", i)
			}
		}
	}
}

// how many servers think a log entry is committed?
func (cfg *config) nCommitted(index int) (int, interface{}) {
	count := 0
	var cmd interface{} = nil
	for i := 0; i < len(cfg.rafts); i++ {
		if cfg.applyErr[i] != "" {
			cfg.t.Fatal(cfg.applyErr[i])
		}

		cfg.mu.Lock()
		cmd1, ok := cfg.logs[i][index]
		cfg.mu.Unlock()

		if ok {
			if count > 0 && cmd != cmd1 {
				cfg.t.Fatalf("committed values do not match: index %v, %v, %v\n",
					index, cmd, cmd1)
			}
			count += 1
			cmd = cmd1
		}
	}
	return count, cmd
}

// wait for at least n servers to commit.
// but don't wait forever.
func (cfg *config) wait(index int, n int, startTerm int) interface{} {
	to := 10 * time.Millisecond
	for iters := 0; iters < 30; iters++ {
		nd, _ := cfg.nCommitted(index)
		if nd >= n {
			break
		}
		time.Sleep(to)
		if to < time.Second {
			to *= 2
		}
		if startTerm > -1 {
			for _, r := range cfg.rafts {
				if t, _ := r.GetState(); t > startTerm {
					// someone has moved on
					// can no longer guarantee that we'll "win"
					return -1
				}
			}
		}
	}
	nd, cmd := cfg.nCommitted(index)
	if nd < n {
		cfg.t.Fatalf("only %d decided for index %d; wanted %d\n",
			nd, index, n)
	}
	return cmd
}

// do a complete agreement.
// it might choose the wrong leader initially,
// and have to re-submit after giving up.
// entirely gives up after about 10 seconds.
// indirectly checks that the servers agree on the
// same value, since nCommitted() checks this,
// as do the threads that read from applyCh.
// returns index.
// if retry==true, may submit the command multiple
// times, in case a leader fails just after Start().
// if retry==false, calls Start() only once, in order
// to simplify the early Lab 2B tests.
func (cfg *config) one(cmd interface{}, expectedServers int, retry bool) int {
	t0 := time.Now()
	starts := 0
	for time.Since(t0).Seconds() < 10 {
		// try all the servers, maybe one is the leader.
		index := -1
		for si := 0; si < cfg.n; si++ {
			starts = (starts + 1) % cfg.n
			var rf *Raft
			cfg.mu.Lock()
			if cfg.connected[starts] {
				rf = cfg.rafts[starts]
			}
			cfg.mu.Unlock()
			if rf != nil {
				index1, _, ok := rf.Start(cmd)
				if ok {
					index = index1
					break
				}
			}
		}

		if index != -1 {
			// somebody claimed to be the leader and to have
			// submitted our command; wait a while for agreement.
			t1 := time.Now()
			for time.Since(t1).Seconds() < 2 {
				nd, cmd1 := cfg.nCommitted(index)
				if nd > 0 && nd >= expectedServers {
					// committed
					if cmd1 == cmd {
						// and it was the command we submitted.
						return index
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if retry == false {
				cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
			}
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
	return -1
}

// start a Test.
// print the Test message.
// e.g. cfg.begin("Test (2B): RPC counts aren't too high")
func (cfg *config) begin(description string) {
	fmt.Printf("%s ...\n", description)
	cfg.t0 = time.Now()
	cfg.rpcs0 = cfg.rpcTotal()
	cfg.bytes0 = cfg.bytesTotal()
	cfg.cmds0 = 0
	cfg.maxIndex0 = cfg.maxIndex
}

// end a Test -- 事实上，程序运行到此表示未发生错误。打印通过信息和一些性能数据
func (cfg *config) end() {
	cfg.checkTimeout()
	if cfg.t.Failed() == false {
		cfg.mu.Lock()
		t := time.Since(cfg.t0).Seconds()       // real time
		npeers := cfg.n                         // number of Raft peers
		nrpc := cfg.rpcTotal() - cfg.rpcs0      // number of RPC sends
		nbytes := cfg.bytesTotal() - cfg.bytes0 // number of bytes
		ncmds := cfg.maxIndex - cfg.maxIndex0   // number of Raft agreements reported
		cfg.mu.Unlock()

		fmt.Printf("  ... Passed --")
		fmt.Printf("  %4.1f  %d %4d %7d %4d\n", t, npeers, nrpc, nbytes, ncmds)
	}
}

// Maximum log size across all servers
func (cfg *config) LogSize() int {
	logsize := 0
	for i := 0; i < cfg.n; i++ {
		n := cfg.saved[i].RaftStateSize()
		if n > logsize {
			logsize = n
		}
	}
	return logsize
}
