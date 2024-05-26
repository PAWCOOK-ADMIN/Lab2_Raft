package labrpc

//
// channel-based RPC, for 824 labs.
//
// simulates a network that can lose requests, lose replies,
// delay messages, and entirely disconnect particular hosts.
//
// we will use the original labrpc.go to test your code for grading.
// so, while you can modify this code to help you debug, please
// test against the original before submitting.
//
// adapted from Go net/rpc/server.go.
//
// sends labgob-encoded values to ensure that RPCs
// don't include references to program objects.
//
// net := MakeNetwork() -- holds network, clients, servers.
// end := net.MakeEnd(endname) -- create a client end-point, to talk to one server.
// net.AddServer(servername, server) -- adds a named server to network.
// net.DeleteServer(servername) -- eliminate the named server.
// net.Connect(endname, servername) -- connect a client to a server.
// net.Enable(endname, enabled) -- enable/disable a client.
// net.Reliable(bool) -- false means drop/delay messages
//
// end.Call("Raft.AppendEntries", &args, &reply) -- send an RPC, wait for reply.
// the "Raft" is the name of the server struct to be called.
// the "AppendEntries" is the name of the method to be called.
// Call() returns true to indicate that the server executed the request
// and the reply is valid.
// Call() returns false if the network lost the request or reply
// or the server is down.
// It is OK to have multiple Call()s in progress at the same time on the
// same ClientEnd.
// Concurrent calls to Call() may be delivered to the server out of order,
// since the network may re-order messages.
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.
// the server RPC handler function must declare its args and reply arguments
// as pointers, so that their types exactly match the types of the arguments
// to Call().
//
// srv := MakeServer()
// srv.AddService(svc) -- a server can have multiple services, e.g. Raft and k/v
//   pass srv to net.AddServer()
//
// svc := MakeService(receiverObject) -- obj's methods will handle RPCs
//   much like Go's rpcs.Register()
//   pass svc to srv.AddService()
//

import "6.824/labgob"
import "bytes"
import "reflect"
import "sync"
import "log"
import "strings"
import "math/rand"
import "time"
import "sync/atomic"

// RPC 请求结构体
type reqMsg struct {
	endname  interface{} // RPC请求的Raft节点（ClientEnd）
	svcMeth  string      // e.g. "Raft.AppendEntries"
	argsType reflect.Type
	args     []byte
	replyCh  chan replyMsg
}

type replyMsg struct {
	ok    bool
	reply []byte
}

// ClientEnd 接收数据的端点，比如节点1，它的端点有：接收节点1数据的端点，接收节点2数据的端点，接收节点3数据的端点
type ClientEnd struct {
	endname interface{}   // 该端点的名称
	ch      chan reqMsg   // 接收rpc请求的通道
	done    chan struct{} // 当网络清理时关闭，用于通知协程停止
}

// send an RPC, wait for the reply.
// the return value indicates success; false means that
// no reply was received from the server.
// svcMeth RPC调用的目标函数
// args RPC调用的参数
// reply 接受响应的变量
func (e *ClientEnd) Call(svcMeth string, args interface{}, reply interface{}) bool {
	req := reqMsg{}
	req.endname = e.endname
	req.svcMeth = svcMeth
	req.argsType = reflect.TypeOf(args)
	req.replyCh = make(chan replyMsg)

	qb := new(bytes.Buffer)                 // 创建一个新的字节缓冲区
	qe := labgob.NewEncoder(qb)             // 创建一个新的 LabEncoder 实例，并将编码后的数据写入 qb
	if err := qe.Encode(args); err != nil { // 将变量编码成二进制，以方便在网上传输
		panic(err)
	}
	req.args = qb.Bytes() // 从缓冲区中提取已经写入的数据

	// 发送请求（将数据写入通道）
	select {
	case e.ch <- req:
		// 请求发送成功
	case <-e.done:
		// 整个网络崩溃了
		return false
	}

	// 等待响应（读管道数据）
	rep := <-req.replyCh

	// 响应成功
	if rep.ok {
		rb := bytes.NewBuffer(rep.reply)         // 创建一个字节缓冲区，并将rep.reply数据写入缓冲区
		rd := labgob.NewDecoder(rb)              // 创建一个解码器，并指明从 rb 中读取数据
		if err := rd.Decode(reply); err != nil { // 解码，并把数据写入 reply 中
			log.Fatalf("ClientEnd.Call(): decode reply: %v\n", err)
		}
		return true
	} else {
		return false
	}
}

type Network struct {
	mu             sync.Mutex
	reliable       bool                        // 网络是否可靠
	longDelays     bool                        // 在一个不可用的连接上发送时暂停一段时间
	longReordering bool                        // sometimes delay replies a long time
	ends           map[interface{}]*ClientEnd  // 网络通信中的端点，key为端点的名称，value为实例（比如，key：wcOrWWIZMDAR-P22WBi3）
	enabled        map[interface{}]bool        // 保存Raft集群中所有端点的状态。key为端点名，value为端点是否可用
	servers        map[interface{}]*Server     // 每台机器上的RPC代理，每个RPC代理中可以注册多个RPC服务
	connections    map[interface{}]interface{} // 端点 -> 端点所属的Raft节点，即是谁的端点。如 C1sppzYPjwOIeMFGQbMd -> 1
	endCh          chan reqMsg                 // 保存网络中所有的RPC请求
	done           chan struct{}               // closed when Network is cleaned up
	count          int32                       // 总的 RPC 请求数量, for statistics
	bytes          int64                       // 总的 RPC 请求字节数, for statistics
}

func MakeNetwork() *Network {
	rn := &Network{}
	rn.reliable = true                               // 初始化网络为可靠
	rn.ends = map[interface{}]*ClientEnd{}           // 初始化客户端端点映射
	rn.enabled = map[interface{}]bool{}              // 初始化启用状态映射
	rn.servers = map[interface{}]*Server{}           // 初始化服务器映射
	rn.connections = map[interface{}](interface{}){} // 初始化连接映射
	rn.endCh = make(chan reqMsg)                     // 创建请求通道
	rn.done = make(chan struct{})                    // 创建完成信号通道

	// 单个协程来处理所有的客户端调用
	go func() {
		for {
			// 多路复用，等待某个通道可读
			select {
			case xreq := <-rn.endCh: // 等待RPC请求
				atomic.AddInt32(&rn.count, 1)                     // 增加请求计数
				atomic.AddInt64(&rn.bytes, int64(len(xreq.args))) // 增加字节数计数
				go rn.processReq(xreq)                            // 处理RPC请求
			case <-rn.done: // 如果网络崩溃
				return
			}
		}
	}()

	return rn
}

func (rn *Network) Cleanup() {
	close(rn.done)
}

func (rn *Network) Reliable(yes bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.reliable = yes
}

func (rn *Network) LongReordering(yes bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.longReordering = yes
}

func (rn *Network) LongDelays(yes bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.longDelays = yes
}

func (rn *Network) readEndnameInfo(endname interface{}) (enabled bool, servername interface{}, server *Server, reliable bool, longreordering bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	enabled = rn.enabled[endname]
	servername = rn.connections[endname]
	if servername != nil {
		server = rn.servers[servername]
	}
	reliable = rn.reliable
	longreordering = rn.longReordering
	return
}

func (rn *Network) isServerDead(endname interface{}, servername interface{}, server *Server) bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if rn.enabled[endname] == false || rn.servers[servername] != server {
		return true
	}
	return false
}

// 处理rpc请求
func (rn *Network) processReq(req reqMsg) {
	enabled, servername, server, reliable, longreordering := rn.readEndnameInfo(req.endname)

	// 如果服务器启用且服务器名和服务器实例不为空
	if enabled && servername != nil && server != nil {
		// 如果不可靠，增加一个短暂的延迟（0-26毫秒）
		if reliable == false {
			ms := (rand.Int() % 27)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}

		// 如果不可靠且有10%的概率，丢弃请求，模拟超时
		if reliable == false && (rand.Int()%1000) < 100 {
			req.replyCh <- replyMsg{false, nil}
			return
		}

		// execute the request (call the RPC handler).
		// in a separate thread so that we can periodically check
		// if the server has been killed and the RPC should get a
		// failure reply.
		// 在单独的goroutine中执行请求（调用RPC处理程序），以便定期检查服务器是否已被杀死，RPC应返回失败响应。
		ech := make(chan replyMsg)
		go func() {
			r := server.dispatch(req) // 执行请求，调用服务器的dispatch方法
			ech <- r                  // 将结果发送到ech通道
		}()

		// 等待处理程序返回，但如果DeleteServer()已被调用则停止等待，返回错误。
		var reply replyMsg
		replyOK := false
		serverDead := false
		for replyOK == false && serverDead == false {
			select {
			case reply = <-ech:
				replyOK = true // 成功接收到处理结果
			case <-time.After(100 * time.Millisecond): // 每100毫秒检查一次服务器是否已被杀死
				serverDead = rn.isServerDead(req.endname, servername, server)
				if serverDead {
					go func() {
						<-ech // 排空通道，以终止前面创建的goroutine
					}()
				}
			}
		}

		// do not reply if DeleteServer() has been called, i.e.
		// the server has been killed. this is needed to avoid
		// situation in which a client gets a positive reply
		// to an Append, but the server persisted the update
		// into the old Persister. config.go is careful to call
		// DeleteServer() before superseding the Persister.
		// 如果DeleteServer()已被调用，即服务器已被杀死，则不回复。需要这样做以避免客户端在追加操作上获得正面回复，但服务器将更新保存在旧的持久存储中。
		serverDead = rn.isServerDead(req.endname, servername, server)

		if replyOK == false || serverDead == true {
			// 等待期间服务器被杀死；返回错误。
			req.replyCh <- replyMsg{false, nil}
		} else if reliable == false && (rand.Int()%1000) < 100 {
			// 丢弃回复，返回超时模拟
			req.replyCh <- replyMsg{false, nil}
		} else if longreordering == true && rand.Intn(900) < 600 {
			// 延迟响应一段时间
			ms := 200 + rand.Intn(1+rand.Intn(2000))
			//通过这种定时器安排减少goroutine数量，以使竞争检测器不太可能出问题
			time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
				atomic.AddInt64(&rn.bytes, int64(len(reply.reply)))
				req.replyCh <- reply
			})
		} else {
			// 正常回复请求
			atomic.AddInt64(&rn.bytes, int64(len(reply.reply)))
			req.replyCh <- reply
		}
	} else {
		// 模拟没有回复并最终超时。
		ms := 0
		if rn.longDelays {
			// 让Raft测试检查leader不会同步发送RPC。
			ms = (rand.Int() % 7000)
		} else {
			// 许多kv测试要求客户端相对快速地尝试每个服务器。
			ms = (rand.Int() % 100)
		}
		time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
			req.replyCh <- replyMsg{false, nil}
		})
	}

}

// create a client end-point.
// start the thread that listens and delivers.
func (rn *Network) MakeEnd(endname interface{}) *ClientEnd {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	if _, ok := rn.ends[endname]; ok {
		log.Fatalf("MakeEnd: %v already exists\n", endname) // 如果端点已经存在，则结束测试
	}

	e := &ClientEnd{}
	e.endname = endname
	e.ch = rn.endCh // 每个端点接收数据的通道，实际上都是Network的通道
	e.done = rn.done
	rn.ends[endname] = e
	rn.enabled[endname] = false
	rn.connections[endname] = nil

	return e
}

func (rn *Network) AddServer(servername interface{}, rs *Server) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.servers[servername] = rs
}

// 删除调用服务，其上的rpc方法变得不可调用
func (rn *Network) DeleteServer(servername interface{}) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.servers[servername] = nil
}

// Connect 为一个Raft节点绑定上端点 ClientEnd
// 一个端点在它的生命周期中只能被连接一次
func (rn *Network) Connect(endname interface{}, servername interface{}) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.connections[endname] = servername
}

// 开启或者关闭一个端点
func (rn *Network) Enable(endname interface{}, enabled bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	rn.enabled[endname] = enabled
}

// get a server's count of incoming RPCs.
func (rn *Network) GetCount(servername interface{}) int {
	rn.mu.Lock()
	defer rn.mu.Unlock()

	svr := rn.servers[servername]
	return svr.GetCount()
}

func (rn *Network) GetTotalCount() int {
	x := atomic.LoadInt32(&rn.count)
	return int(x)
}

func (rn *Network) GetTotalBytes() int64 {
	x := atomic.LoadInt64(&rn.bytes)
	return x
}

// Server 是服务的集合，共享相同的 RPC 分发器。
// 例如，一个 Raft 和一个 k/v 服务器可以监听相同的 RPC 端点。
type Server struct {
	mu       sync.Mutex          // 互斥锁，用于保护并发访问
	services map[string]*Service // 服务映射，键是服务名称，值是服务实例（一个机器上可以有多个RPC服务，每个服务可以有多个函数）
	count    int                 // 记录接收到的 RPC 数量
}

func MakeServer() *Server {
	rs := &Server{}
	rs.services = map[string]*Service{}
	return rs
}

func (rs *Server) AddService(svc *Service) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.services[svc.name] = svc
}

func (rs *Server) dispatch(req reqMsg) replyMsg {
	rs.mu.Lock()

	rs.count += 1

	// split Raft.AppendEntries into service and method
	dot := strings.LastIndex(req.svcMeth, ".")
	serviceName := req.svcMeth[:dot]
	methodName := req.svcMeth[dot+1:]

	service, ok := rs.services[serviceName]

	rs.mu.Unlock()

	if ok {
		return service.dispatch(methodName, req)
	} else {
		choices := []string{}
		for k, _ := range rs.services {
			choices = append(choices, k)
		}
		log.Fatalf("labrpc.Server.dispatch(): unknown service %v in %v.%v; expecting one of %v\n",
			serviceName, serviceName, methodName, choices)
		return replyMsg{false, nil}
	}
}

func (rs *Server) GetCount() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.count
}

// 定义一个Service结构体，包含可以通过RPC调用的方法。
// 一个服务器可能有多个Service。
type Service struct {
	name    string                    // 服务名称。如 “Raft”
	rcvr    reflect.Value             // 接收者的反射值
	typ     reflect.Type              // 接收者的反射类型
	methods map[string]reflect.Method // 可通过RPC调用的方法集合
}

// 创建一个新的Service实例。
func MakeService(rcvr interface{}) *Service {
	svc := &Service{}
	svc.typ = reflect.TypeOf(rcvr)                      // 获取接收者的反射类型
	svc.rcvr = reflect.ValueOf(rcvr)                    // 获取接收者的反射值
	svc.name = reflect.Indirect(svc.rcvr).Type().Name() // 获取接收者的非指针类型名称
	svc.methods = map[string]reflect.Method{}           // 初始化方法映射

	// 遍历Raft结构体类型中的所有方法
	for m := 0; m < svc.typ.NumMethod(); m++ {
		method := svc.typ.Method(m) // 获取方法
		mtype := method.Type        // 获取方法的类型
		mname := method.Name        // 获取方法的名称

		// 检查方法是否适合作为处理程序
		// - 检查方法是否导出（首字母是否大写）
		// - 方法必须有三个参数（接收者、请求参数、回复参数）
		// - 第二个参数必须是指针类型
		// - 方法不能有返回值
		if method.PkgPath != "" || mtype.NumIn() != 3 || mtype.In(2).Kind() != reflect.Ptr || mtype.NumOut() != 0 {
			// 方法不适合作为处理程序
			//fmt.Printf("bad method: %v\n", mname)
		} else {
			// 将方法添加到方法映射中
			svc.methods[mname] = method
		}
	}

	return svc
}

func (svc *Service) dispatch(methname string, req reqMsg) replyMsg {
	if method, ok := svc.methods[methname]; ok {
		// prepare space into which to read the argument.
		// the Value's type will be a pointer to req.argsType.
		args := reflect.New(req.argsType)

		// decode the argument.
		ab := bytes.NewBuffer(req.args)
		ad := labgob.NewDecoder(ab)
		ad.Decode(args.Interface())

		// allocate space for the reply.
		replyType := method.Type.In(2)
		replyType = replyType.Elem()
		replyv := reflect.New(replyType)

		// call the method.
		function := method.Func
		function.Call([]reflect.Value{svc.rcvr, args.Elem(), replyv})

		// encode the reply.
		rb := new(bytes.Buffer)
		re := labgob.NewEncoder(rb)
		re.EncodeValue(replyv)

		return replyMsg{true, rb.Bytes()}
	} else {
		choices := []string{}
		for k, _ := range svc.methods {
			choices = append(choices, k)
		}
		log.Fatalf("labrpc.Service.dispatch(): unknown method %v in %v; expecting one of %v\n",
			methname, req.svcMeth, choices)
		return replyMsg{false, nil}
	}
}
