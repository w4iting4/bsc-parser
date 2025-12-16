package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

// 配置结构体定义
type Config struct {
	Benchmark  BenchmarkConfig  `yaml:"benchmark"`
	Network    NetworkConfig    `yaml:"network"`
	Nodes      []NodeConfig     `yaml:"nodes"`
	Statistics StatisticsConfig `yaml:"statistics"`
	Output     OutputConfig     `yaml:"output"`
}

type BenchmarkConfig struct {
	Duration          time.Duration `yaml:"duration"`
	ReportInterval    time.Duration `yaml:"report_interval"`
	ConnectionTimeout time.Duration `yaml:"connection_timeout"`
}

type NetworkConfig struct {
	ChainID int    `yaml:"chain_id"`
	Name    string `yaml:"name"`
}

type NodeConfig struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url"`
	Description string `yaml:"description"`
}

type StatisticsConfig struct {
	TrackBlockGaps  bool `yaml:"track_block_gaps"`
	CalculateLatency bool `yaml:"calculate_latency"`
	ShowRawData     bool `yaml:"show_raw_data"`
}

type OutputConfig struct {
	ShowRealtime       bool   `yaml:"show_realtime"`
	ShowPeriodicReport bool   `yaml:"show_periodic_report"`
	ShowFinalSummary   bool   `yaml:"show_final_summary"`
	TimestampFormat    string `yaml:"timestamp_format"`
}

// ============== 测速数据结构定义 ==============

// 区块信息
type BlockInfo struct {
	Number      uint64    // 区块号
	Hash        string    // 区块哈希
	Timestamp   time.Time // 区块时间戳
	ReceiveTime time.Time // 本地接收时间（毫秒精度）
	NodeName    string    // 接收节点名称
}

// 节点状态
type NodeStatus struct {
	Name           string        // 节点名称
	URL            string        // 节点URL
	IsConnected    bool          // 连接状态
	LastBlockNum   uint64        // 最后接收的区块号
	LastReceiveTime time.Time    // 最后接收时间
	ConnectTime    time.Time     // 连接建立时间
	DisconnectTime time.Time     // 断连时间
	ReconnectCount int           // 重连次数
	TotalBlocks    int           // 收到的总区块数
}

// 区块竞速记录
type BlockRace struct {
	BlockNumber uint64                // 区块号
	FirstNode   string                // 首达节点
	FirstTime   time.Time             // 首达时间
	AllNodes    map[string]time.Time  // 所有节点到达时间
	Delays      map[string]time.Duration // 各节点相对首达的延迟
}

// 统计数据
type Statistics struct {
	StartTime       time.Time                 // 测试开始时间
	EndTime         time.Time                 // 测试结束时间
	TotalBlocks     int                       // 总收到区块数
	NodeStats       map[string]*NodeStats     // 各节点统计
	BlockRaces      []BlockRace               // 区块竞速记录
	FirstNodeCounts map[string]int            // 各节点首达次数
	mutex           sync.RWMutex              // 读写锁
}

// 节点统计
type NodeStats struct {
	Name            string        // 节点名称
	TotalBlocks     int           // 总接收区块数
	FirstBlocks     int           // 首达区块数
	AverageDelay    time.Duration // 平均延迟
	MaxDelay        time.Duration // 最大延迟
	MinDelay        time.Duration // 最小延迟
	MissedBlocks    int           // 遗漏区块数
	ConnectionTime  time.Duration // 总连接时间
	ReconnectCount  int           // 重连次数
}

// 节点连接信息
type NodeConnection struct {
	NodeName   string
	Client     *ethclient.Client
	IsConnected bool
	ConnectedAt time.Time
	LastError   error
}

// 测速管理器
type SpeedTester struct {
	Config       *Config
	Nodes        map[string]*NodeStatus
	Connections  map[string]*NodeConnection  // WebSocket连接
	Statistics   *Statistics
	BlockChan    chan BlockInfo        // 区块信息通道
	StopChan     chan bool            // 停止信号通道
	ReportTicker *time.Ticker         // 报告定时器
	mutex        sync.RWMutex         // 读写锁
}

// 创建新的测速管理器
func NewSpeedTester(config *Config) *SpeedTester {
	st := &SpeedTester{
		Config:      config,
		Nodes:       make(map[string]*NodeStatus),
		Connections: make(map[string]*NodeConnection),
		BlockChan:   make(chan BlockInfo, 1000), // 缓冲区
		StopChan:    make(chan bool),
		Statistics: &Statistics{
			StartTime:       time.Now(),
			NodeStats:       make(map[string]*NodeStats),
			BlockRaces:      make([]BlockRace, 0),
			FirstNodeCounts: make(map[string]int),
		},
	}
	
	// 初始化节点状态
	for _, nodeConfig := range config.Nodes {
		st.Nodes[nodeConfig.Name] = &NodeStatus{
			Name:           nodeConfig.Name,
			URL:            nodeConfig.URL,
			IsConnected:    false,
			ReconnectCount: 0,
			TotalBlocks:    0,
		}
		
		// 初始化连接状态
		st.Connections[nodeConfig.Name] = &NodeConnection{
			NodeName:    nodeConfig.Name,
			Client:      nil,
			IsConnected: false,
		}
		
		// 初始化节点统计
		st.Statistics.NodeStats[nodeConfig.Name] = &NodeStats{
			Name:        nodeConfig.Name,
			MinDelay:    time.Hour, // 初始化为大值
		}
	}
	
	return st
}

// 记录区块信息
func (st *SpeedTester) RecordBlock(blockInfo BlockInfo) {
	st.Statistics.mutex.Lock()
	defer st.Statistics.mutex.Unlock()
	
	// 更新节点状态
	if nodeStatus, exists := st.Nodes[blockInfo.NodeName]; exists {
		nodeStatus.LastBlockNum = blockInfo.Number
		nodeStatus.LastReceiveTime = blockInfo.ReceiveTime
		nodeStatus.TotalBlocks++
	}
	
	// 检查是否是新区块
	blockNum := blockInfo.Number
	var race *BlockRace
	
	// 查找现有的区块竞速记录
	for i := range st.Statistics.BlockRaces {
		if st.Statistics.BlockRaces[i].BlockNumber == blockNum {
			race = &st.Statistics.BlockRaces[i]
			break
		}
	}
	
	// 如果是新区块，创建竞速记录
	if race == nil {
		newRace := BlockRace{
			BlockNumber: blockNum,
			FirstNode:   blockInfo.NodeName,
			FirstTime:   blockInfo.ReceiveTime,
			AllNodes:    make(map[string]time.Time),
			Delays:      make(map[string]time.Duration),
		}
		st.Statistics.BlockRaces = append(st.Statistics.BlockRaces, newRace)
		race = &st.Statistics.BlockRaces[len(st.Statistics.BlockRaces)-1]
		
		// 更新首达统计
		st.Statistics.FirstNodeCounts[blockInfo.NodeName]++
		st.Statistics.NodeStats[blockInfo.NodeName].FirstBlocks++
	}
	
	// 记录节点到达时间
	race.AllNodes[blockInfo.NodeName] = blockInfo.ReceiveTime
	
	// 计算延迟
	delay := blockInfo.ReceiveTime.Sub(race.FirstTime)
	race.Delays[blockInfo.NodeName] = delay
	
	// 更新节点统计
	nodeStats := st.Statistics.NodeStats[blockInfo.NodeName]
	nodeStats.TotalBlocks++
	
	if delay < nodeStats.MinDelay {
		nodeStats.MinDelay = delay
	}
	if delay > nodeStats.MaxDelay {
		nodeStats.MaxDelay = delay
	}
	
	st.Statistics.TotalBlocks++
}

// 连接单个节点
func (st *SpeedTester) connectNode(nodeName string, nodeURL string) error {
	fmt.Printf("🔗 正在连接节点: %s\n", nodeName)
	fmt.Printf("   URL: %s\n", nodeURL)
	
	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), st.Config.Benchmark.ConnectionTimeout)
	defer cancel()
	
	// 连接到WebSocket端点
	client, err := ethclient.DialContext(ctx, nodeURL)
	if err != nil {
		fmt.Printf("   ❌ 连接失败: %v\n", err)
		
		// 更新连接状态
		st.mutex.Lock()
		if conn, exists := st.Connections[nodeName]; exists {
			conn.IsConnected = false
			conn.LastError = err
		}
		if nodeStatus, exists := st.Nodes[nodeName]; exists {
			nodeStatus.IsConnected = false
			nodeStatus.ReconnectCount++
		}
		st.mutex.Unlock()
		
		return err
	}
	
	// 测试连接 - 获取链ID
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		client.Close()
		fmt.Printf("   ❌ 链ID验证失败: %v\n", err)
		return err
	}
	
	// 验证链ID是否正确
	if chainID.Uint64() != uint64(st.Config.Network.ChainID) {
		client.Close()
		err := fmt.Errorf("链ID不匹配: 期望%d, 实际%d", st.Config.Network.ChainID, chainID.Uint64())
		fmt.Printf("   ❌ %v\n", err)
		return err
	}
	
	// 获取最新区块号测试连接
	latestBlock, err := client.BlockNumber(context.Background())
	if err != nil {
		client.Close()
		fmt.Printf("   ❌ 获取区块号失败: %v\n", err)
		return err
	}
	
	// 更新连接状态
	connectTime := time.Now()
	st.mutex.Lock()
	if conn, exists := st.Connections[nodeName]; exists {
		conn.Client = client
		conn.IsConnected = true
		conn.ConnectedAt = connectTime
		conn.LastError = nil
	}
	if nodeStatus, exists := st.Nodes[nodeName]; exists {
		nodeStatus.IsConnected = true
		nodeStatus.ConnectTime = connectTime
		nodeStatus.LastBlockNum = latestBlock
	}
	st.mutex.Unlock()
	
	fmt.Printf("   ✅ 连接成功!\n")
	fmt.Printf("   ⛓️  链ID: %d\n", chainID.Uint64())
	fmt.Printf("   📦 最新区块: #%d\n", latestBlock)
	fmt.Printf("   ⏰ 连接时间: %s\n", connectTime.Format("15:04:05.000"))
	
	return nil
}

// 并发连接所有节点
func (st *SpeedTester) ConnectAllNodes() {
	fmt.Println("\n🌐 开始连接所有节点...")
	
	var wg sync.WaitGroup
	
	// 为每个节点启动连接goroutine
	for _, nodeConfig := range st.Config.Nodes {
		wg.Add(1)
		
		go func(name, url string) {
			defer wg.Done()
			
			err := st.connectNode(name, url)
			if err != nil {
				fmt.Printf("⚠️  节点 %s 连接失败，将跳过监听\n", name)
			}
		}(nodeConfig.Name, nodeConfig.URL)
	}
	
	// 等待所有连接完成
	wg.Wait()
	
	// 统计连接结果
	connectedCount := 0
	totalCount := len(st.Config.Nodes)
	
	st.mutex.RLock()
	for _, conn := range st.Connections {
		if conn.IsConnected {
			connectedCount++
		}
	}
	st.mutex.RUnlock()
	
	fmt.Printf("\n📊 连接结果: %d/%d 节点连接成功\n", connectedCount, totalCount)
	
	if connectedCount == 0 {
		log.Fatal("❌ 没有任何节点连接成功，无法继续测试")
	}
	
	fmt.Println("✅ 节点连接阶段完成!")
}

// 断开所有连接
func (st *SpeedTester) DisconnectAllNodes() {
	fmt.Println("\n🔌 断开所有节点连接...")
	
	st.mutex.Lock()
	defer st.mutex.Unlock()
	
	for nodeName, conn := range st.Connections {
		if conn.IsConnected && conn.Client != nil {
			conn.Client.Close()
			conn.IsConnected = false
			fmt.Printf("   ✅ %s 连接已断开\n", nodeName)
		}
		
		if nodeStatus, exists := st.Nodes[nodeName]; exists {
			nodeStatus.IsConnected = false
			nodeStatus.DisconnectTime = time.Now()
		}
	}
	
	fmt.Println("✅ 所有连接已断开")
}

// 获取连接状态摘要
func (st *SpeedTester) GetConnectionSummary() map[string]bool {
	st.mutex.RLock()
	defer st.mutex.RUnlock()
	
	summary := make(map[string]bool)
	for nodeName, conn := range st.Connections {
		summary[nodeName] = conn.IsConnected
	}
	
	return summary
}

// 监听单个节点的新区块头
func (st *SpeedTester) listenNodeBlocks(nodeName string) {
	st.mutex.RLock()
	conn, exists := st.Connections[nodeName]
	if !exists || !conn.IsConnected || conn.Client == nil {
		st.mutex.RUnlock()
		fmt.Printf("⚠️  节点 %s 未连接，跳过监听\n", nodeName)
		return
	}
	client := conn.Client
	st.mutex.RUnlock()
	
	fmt.Printf("🎯 开始监听节点: %s\n", nodeName)
	
	// 创建新区块头订阅
	headers := make(chan *types.Header)
	sub, err := client.SubscribeNewHead(context.Background(), headers)
	if err != nil {
		fmt.Printf("❌ 节点 %s 订阅失败: %v\n", nodeName, err)
		return
	}
	defer sub.Unsubscribe()
	
	fmt.Printf("✅ 节点 %s 订阅成功，等待新区块...\n", nodeName)
	
	for {
		select {
		case err := <-sub.Err():
			fmt.Printf("❌ 节点 %s 订阅错误: %v\n", nodeName, err)
			return
			
		case header := <-headers:
			// 记录区块到达的精确时间
			receiveTime := time.Now()
			
			// 创建区块信息
			blockInfo := BlockInfo{
				Number:      header.Number.Uint64(),
				Hash:        header.Hash().Hex(),
				Timestamp:   time.Unix(int64(header.Time), 0),
				ReceiveTime: receiveTime,
				NodeName:    nodeName,
			}
			
			// 发送到区块通道
			select {
			case st.BlockChan <- blockInfo:
				// 区块信息成功发送到通道
			default:
				// 通道已满，丢弃区块信息
				fmt.Printf("⚠️  区块通道已满，丢弃区块 #%d 来自 %s\n", blockInfo.Number, nodeName)
			}
			
		case <-st.StopChan:
			fmt.Printf("🛑 节点 %s 监听已停止\n", nodeName)
			return
		}
	}
}

// 启动所有节点的区块监听
func (st *SpeedTester) StartBlockListening() {
	fmt.Println("\n🎯 启动所有节点的区块监听...")
	
	connectedNodes := 0
	
	// 为每个连接的节点启动监听goroutine
	for nodeName, conn := range st.Connections {
		if conn.IsConnected && conn.Client != nil {
			connectedNodes++
			go st.listenNodeBlocks(nodeName)
		} else {
			fmt.Printf("⚪ 节点 %s 未连接，跳过监听\n", nodeName)
		}
	}
	
	if connectedNodes == 0 {
		log.Fatal("❌ 没有任何节点可用于监听")
	}
	
	fmt.Printf("✅ 已启动 %d 个节点的区块监听\n", connectedNodes)
}

// 处理接收到的区块信息
func (st *SpeedTester) ProcessBlocks() {
	fmt.Println("🔄 开始处理区块信息...")
	
	for {
		select {
		case blockInfo := <-st.BlockChan:
			// 记录区块信息
			st.RecordBlock(blockInfo)
			
			// 实时输出（如果配置启用）
			if st.Config.Output.ShowRealtime {
				fmt.Printf("📦 新区块 #%d 来自 %s (时间: %s)\n", 
					blockInfo.Number, 
					blockInfo.NodeName, 
					blockInfo.ReceiveTime.Format(st.Config.Output.TimestampFormat))
				
				// 显示当前区块的增强竞速情况
				st.showEnhancedBlockRaceInfo(blockInfo.Number)
			}
			
		case <-st.StopChan:
			fmt.Println("🛑 区块处理已停止")
			return
		}
	}
}

// 显示单个区块的竞速信息
func (st *SpeedTester) showBlockRaceInfo(blockNumber uint64) {
	st.Statistics.mutex.RLock()
	defer st.Statistics.mutex.RUnlock()
	
	// 查找对应的区块竞速记录
	for _, race := range st.Statistics.BlockRaces {
		if race.BlockNumber == blockNumber {
			if len(race.AllNodes) > 1 {
				fmt.Printf("   🏁 竞速: 首达节点 %s", race.FirstNode)
				
				// 显示其他节点的延迟
				for nodeName, delay := range race.Delays {
					if nodeName != race.FirstNode && delay > 0 {
						fmt.Printf(", %s (+%dms)", nodeName, delay.Milliseconds())
					}
				}
				fmt.Println()
			}
			break
		}
	}
}

// 启动完整的测速测试
func (st *SpeedTester) StartSpeedTest() {
	fmt.Println("\n🚀 开始完整测速测试...")
	
	// 启动区块处理协程
	go st.ProcessBlocks()
	
	// 启动周期性报告协程
	go st.startPeriodicReporting()
	
	// 启动区块监听
	st.StartBlockListening()
	
	// 等待指定时间
	fmt.Printf("⏰ 测试将运行 %v...\n", st.Config.Benchmark.Duration)
	
	// 创建定时器
	testTimer := time.NewTimer(st.Config.Benchmark.Duration)
	defer testTimer.Stop()
	
	// 等待测试完成
	<-testTimer.C
	
	fmt.Println("\n⏰ 测试时间到，正在停止...")
	
	// 发送停止信号
	close(st.StopChan)
	
	// 等待一点时间让所有goroutine处理完成
	time.Sleep(1 * time.Second)
	
	fmt.Println("✅ 测速测试完成!")
}

// 计算平均延迟
func (st *SpeedTester) calculateAverageDelays() {
	st.Statistics.mutex.Lock()
	defer st.Statistics.mutex.Unlock()
	
	// 为每个节点计算平均延迟
	delaySum := make(map[string]time.Duration)
	delayCount := make(map[string]int)
	
	for _, race := range st.Statistics.BlockRaces {
		for nodeName, delay := range race.Delays {
			delaySum[nodeName] += delay
			delayCount[nodeName]++
		}
	}
	
	// 更新节点统计
	for nodeName, nodeStats := range st.Statistics.NodeStats {
		if count, exists := delayCount[nodeName]; exists && count > 0 {
			nodeStats.AverageDelay = delaySum[nodeName] / time.Duration(count)
		}
	}
}

// 周期性报告
func (st *SpeedTester) generatePeriodicReport() {
	st.calculateAverageDelays()
	
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Printf("📊 周期性报告 - %s\n", time.Now().Format("15:04:05"))
	fmt.Println(strings.Repeat("=", 60))
	
	st.Statistics.mutex.RLock()
	defer st.Statistics.mutex.RUnlock()
	
	// 总体统计
	fmt.Printf("📈 总体统计:\n")
	fmt.Printf("   - 总区块数: %d\n", st.Statistics.TotalBlocks)
	fmt.Printf("   - 竞速记录数: %d\n", len(st.Statistics.BlockRaces))
	
	// 计算测试时长
	elapsed := time.Since(st.Statistics.StartTime)
	fmt.Printf("   - 已运行时间: %v\n", elapsed.Truncate(time.Second))
	
	if len(st.Statistics.BlockRaces) > 0 {
		avgBlockTime := elapsed / time.Duration(len(st.Statistics.BlockRaces))
		fmt.Printf("   - 平均出块时间: %v\n", avgBlockTime.Truncate(time.Millisecond))
	}
	
	// 节点排行榜
	fmt.Println("\n🏆 节点排行榜:")
	
	// 按首达次数排序
	type nodeRank struct {
		name       string
		firstCount int
		avgDelay   time.Duration
		maxDelay   time.Duration
		minDelay   time.Duration
		totalBlocks int
	}
	
	var ranks []nodeRank
	for nodeName, nodeStats := range st.Statistics.NodeStats {
		if nodeStats.TotalBlocks > 0 {
			ranks = append(ranks, nodeRank{
				name:        nodeName,
				firstCount:  nodeStats.FirstBlocks,
				avgDelay:    nodeStats.AverageDelay,
				maxDelay:    nodeStats.MaxDelay,
				minDelay:    nodeStats.MinDelay,
				totalBlocks: nodeStats.TotalBlocks,
			})
		}
	}
	
	// 按首达次数降序排序
	for i := 0; i < len(ranks); i++ {
		for j := i + 1; j < len(ranks); j++ {
			if ranks[j].firstCount > ranks[i].firstCount {
				ranks[i], ranks[j] = ranks[j], ranks[i]
			}
		}
	}
	
	// 显示排行榜
	for i, rank := range ranks {
		medal := "🥉"
		if i == 0 {
			medal = "🥇"
		} else if i == 1 {
			medal = "🥈"
		}
		
		fmt.Printf("   %s %d. %s:\n", medal, i+1, rank.name)
		fmt.Printf("      - 首达次数: %d/%d (%.1f%%)\n", 
			rank.firstCount, 
			len(st.Statistics.BlockRaces),
			float64(rank.firstCount)/float64(len(st.Statistics.BlockRaces))*100)
		fmt.Printf("      - 总区块数: %d\n", rank.totalBlocks)
		fmt.Printf("      - 平均延迟: %v\n", rank.avgDelay.Truncate(time.Millisecond))
		
		if rank.maxDelay > 0 {
			fmt.Printf("      - 最大延迟: %v\n", rank.maxDelay.Truncate(time.Millisecond))
		}
		if rank.minDelay < time.Hour {
			fmt.Printf("      - 最小延迟: %v\n", rank.minDelay.Truncate(time.Millisecond))
		}
	}
	
	// 连接状态
	fmt.Println("\n📡 连接状态:")
	for nodeName, conn := range st.Connections {
		status := "❌ 离线"
		if conn.IsConnected {
			status = "✅ 在线"
			uptime := time.Since(conn.ConnectedAt)
			fmt.Printf("   %s: %s (运行时间: %v)\n", nodeName, status, uptime.Truncate(time.Second))
		} else {
			fmt.Printf("   %s: %s\n", nodeName, status)
		}
	}
	
	fmt.Println(strings.Repeat("=", 60))
}

// 增强版实时竞速显示
func (st *SpeedTester) showEnhancedBlockRaceInfo(blockNumber uint64) {
	st.Statistics.mutex.RLock()
	defer st.Statistics.mutex.RUnlock()
	
	// 查找对应的区块竞速记录
	for _, race := range st.Statistics.BlockRaces {
		if race.BlockNumber == blockNumber {
			nodeCount := len(race.AllNodes)
			
			if nodeCount == 1 {
				// 只有一个节点，显示简单信息
				fmt.Printf("   📡 单节点接收\n")
			} else if nodeCount > 1 {
				// 多节点竞速，显示详细对比
				fmt.Printf("   🏁 %d节点竞速: ", nodeCount)
				
				// 找到最快的节点和最大延迟
				var fastest string
				var maxDelay time.Duration
				
				for nodeName, delay := range race.Delays {
					if delay == 0 {
						fastest = nodeName
					}
					if delay > maxDelay {
						maxDelay = delay
					}
				}
				
				fmt.Printf("🥇 %s", fastest)
				
				// 显示其他节点的延迟
				delays := make([]string, 0, nodeCount-1)
				for nodeName, delay := range race.Delays {
					if nodeName != fastest && delay > 0 {
						delays = append(delays, fmt.Sprintf("%s (+%dms)", nodeName, delay.Milliseconds()))
					}
				}
				
				if len(delays) > 0 {
					fmt.Printf(" | %s", strings.Join(delays, ", "))
				}
				
				// 显示最大延迟差
				if maxDelay > 0 {
					fmt.Printf(" | 最大差距: %dms", maxDelay.Milliseconds())
				}
				
				fmt.Println()
			}
			break
		}
	}
}

// 最终详细报告
func (st *SpeedTester) GenerateFinalReport() {
	st.calculateAverageDelays()
	
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Printf("🎯 最终测速报告 - %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("=", 80))
	
	st.Statistics.mutex.RLock()
	defer st.Statistics.mutex.RUnlock()
	
	// 测试概要
	st.Statistics.EndTime = time.Now()
	totalDuration := st.Statistics.EndTime.Sub(st.Statistics.StartTime)
	
	fmt.Printf("📋 测试概要:\n")
	fmt.Printf("   - 开始时间: %s\n", st.Statistics.StartTime.Format("15:04:05"))
	fmt.Printf("   - 结束时间: %s\n", st.Statistics.EndTime.Format("15:04:05"))
	fmt.Printf("   - 总测试时长: %v\n", totalDuration.Truncate(time.Second))
	fmt.Printf("   - 总接收区块: %d\n", st.Statistics.TotalBlocks)
	fmt.Printf("   - 有效竞速记录: %d\n", len(st.Statistics.BlockRaces))
	
	if len(st.Statistics.BlockRaces) > 0 {
		avgBlockInterval := totalDuration / time.Duration(len(st.Statistics.BlockRaces))
		fmt.Printf("   - 平均出块间隔: %v\n", avgBlockInterval.Truncate(time.Millisecond))
	}
	
	// 详细节点分析
	fmt.Println("\n📊 详细节点分析:")
	
	activeNodes := 0
	for nodeName, nodeStats := range st.Statistics.NodeStats {
		if nodeStats.TotalBlocks > 0 {
			activeNodes++
			
			fmt.Printf("\n   🔍 %s:\n", nodeName)
			fmt.Printf("      📦 总接收区块: %d\n", nodeStats.TotalBlocks)
			fmt.Printf("      🥇 首达次数: %d (%.1f%%)\n", 
				nodeStats.FirstBlocks,
				float64(nodeStats.FirstBlocks)/float64(len(st.Statistics.BlockRaces))*100)
			
			if nodeStats.AverageDelay > 0 {
				fmt.Printf("      ⏱️  平均延迟: %v\n", nodeStats.AverageDelay.Truncate(time.Millisecond))
			} else {
				fmt.Printf("      ⏱️  平均延迟: 0ms (首达节点)\n")
			}
			
			if nodeStats.MaxDelay > 0 {
				fmt.Printf("      📈 最大延迟: %v\n", nodeStats.MaxDelay.Truncate(time.Millisecond))
			}
			
			if nodeStats.MinDelay < time.Hour {
				fmt.Printf("      📉 最小延迟: %v\n", nodeStats.MinDelay.Truncate(time.Millisecond))
			}
			
			// 连接稳定性
			if conn, exists := st.Connections[nodeName]; exists {
				if conn.IsConnected || !conn.ConnectedAt.IsZero() {
					uptime := totalDuration
					if !conn.ConnectedAt.IsZero() {
						if conn.IsConnected {
							uptime = time.Since(conn.ConnectedAt)
						} else {
							// 如果已断开，计算到断开时间的连接时长
							if nodeStatus, exists := st.Nodes[nodeName]; exists && !nodeStatus.DisconnectTime.IsZero() {
								uptime = nodeStatus.DisconnectTime.Sub(conn.ConnectedAt)
							}
						}
					}
					stability := float64(uptime) / float64(totalDuration) * 100
					fmt.Printf("      🔗 连接稳定性: %.1f%% (在线时长: %v)\n", 
						stability, uptime.Truncate(time.Second))
				}
			}
		}
	}
	
	// 推荐建议
	fmt.Println("\n💡 测速结论:")
	
	if activeNodes == 0 {
		fmt.Println("   ❌ 没有活跃节点，无法生成建议")
	} else if activeNodes == 1 {
		fmt.Println("   ⚠️  只有一个活跃节点，无法进行性能对比")
		for nodeName, nodeStats := range st.Statistics.NodeStats {
			if nodeStats.TotalBlocks > 0 {
				fmt.Printf("   📡 唯一活跃节点: %s (接收了 %d 个区块)\n", nodeName, nodeStats.TotalBlocks)
			}
		}
	} else {
		// 找出最佳节点
		var bestNode string
		var maxFirstRate float64
		var minAvgDelay time.Duration = time.Hour
		
		for nodeName, nodeStats := range st.Statistics.NodeStats {
			if nodeStats.TotalBlocks > 0 {
				firstRate := float64(nodeStats.FirstBlocks) / float64(len(st.Statistics.BlockRaces))
				if firstRate > maxFirstRate || (firstRate == maxFirstRate && nodeStats.AverageDelay < minAvgDelay) {
					maxFirstRate = firstRate
					minAvgDelay = nodeStats.AverageDelay
					bestNode = nodeName
				}
			}
		}
		
		fmt.Printf("   🏆 推荐节点: %s\n", bestNode)
		fmt.Printf("   📊 首达率: %.1f%%, 平均延迟: %v\n", 
			maxFirstRate*100, minAvgDelay.Truncate(time.Millisecond))
		
		// 性能差距分析
		if len(st.Statistics.BlockRaces) > 1 {
			totalDelayDiff := time.Duration(0)
			maxDelayDiff := time.Duration(0)
			validComparisons := 0
			
			for _, race := range st.Statistics.BlockRaces {
				if len(race.AllNodes) > 1 {
					var maxDelay time.Duration
					for _, delay := range race.Delays {
						if delay > maxDelay {
							maxDelay = delay
						}
					}
					totalDelayDiff += maxDelay
					if maxDelay > maxDelayDiff {
						maxDelayDiff = maxDelay
					}
					validComparisons++
				}
			}
			
			if validComparisons > 0 {
				avgDelayDiff := totalDelayDiff / time.Duration(validComparisons)
				fmt.Printf("   📈 节点间平均延迟差: %v\n", avgDelayDiff.Truncate(time.Millisecond))
				fmt.Printf("   📊 最大延迟差: %v\n", maxDelayDiff.Truncate(time.Millisecond))
			}
		}
	}
	
	fmt.Println(strings.Repeat("=", 80))
}

// 启动周期性报告
func (st *SpeedTester) startPeriodicReporting() {
	if !st.Config.Output.ShowPeriodicReport {
		return
	}
	
	ticker := time.NewTicker(st.Config.Benchmark.ReportInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			st.generatePeriodicReport()
		case <-st.StopChan:
			return
		}
	}
}

// 加载配置文件
func loadConfig() (*Config, error) {
	fmt.Println("📄 正在加载配置文件: config.yaml")
	
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}
	
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}
	
	// 基础验证
	if len(config.Nodes) == 0 {
		return nil, fmt.Errorf("至少需要配置一个节点")
	}
	
	fmt.Printf("✅ 配置加载成功: %s\n", config.Network.Name)
	fmt.Printf("📊 测试节点数: %d\n", len(config.Nodes))
	fmt.Printf("⏱️  测试时长: %v\n", config.Benchmark.Duration)
	fmt.Printf("📈 报告间隔: %v\n", config.Benchmark.ReportInterval)
	
	return &config, nil
}

func main() {
	fmt.Println("🚀 BSC节点测速工具 - 步骤6: 统计功能")
	fmt.Printf("⏰ 启动时间: %s\n", time.Now().Format("2006-01-02 15:04:05.000"))
	
	// 加载配置文件
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	
	// 显示测试配置
	fmt.Printf("\n⚙️  测试配置:\n")
	fmt.Printf("   - 测试时长: %v\n", config.Benchmark.Duration)
	fmt.Printf("   - 报告间隔: %v\n", config.Benchmark.ReportInterval)
	fmt.Printf("   - 连接超时: %v\n", config.Benchmark.ConnectionTimeout)
	fmt.Printf("   - 实时显示: %v\n", config.Output.ShowRealtime)
	
	// 显示节点列表
	fmt.Println("\n🌐 测试节点列表:")
	for i, node := range config.Nodes {
		fmt.Printf("   %d. %s\n", i+1, node.Name)
		fmt.Printf("      URL: %s\n", node.URL)
		fmt.Printf("      描述: %s\n", node.Description)
		fmt.Println()
	}
	
	// 创建测速管理器
	fmt.Println("🏗️  创建测速管理器...")
	speedTester := NewSpeedTester(config)
	
	// 显示数据结构初始化状态
	fmt.Printf("✅ 测速管理器创建成功!\n")
	fmt.Printf("📊 初始化状态:\n")
	fmt.Printf("   - 节点数量: %d\n", len(speedTester.Nodes))
	fmt.Printf("   - 连接数量: %d\n", len(speedTester.Connections))
	fmt.Printf("   - 区块缓冲区大小: %d\n", cap(speedTester.BlockChan))
	fmt.Printf("   - 统计开始时间: %s\n", speedTester.Statistics.StartTime.Format("15:04:05.000"))
	
	// 开始连接所有节点
	speedTester.ConnectAllNodes()
	
	// 检查连接结果
	connectedCount := 0
	connectionSummary := speedTester.GetConnectionSummary()
	for _, isConnected := range connectionSummary {
		if isConnected {
			connectedCount++
		}
	}
	
	if connectedCount == 0 {
		log.Fatal("❌ 没有任何节点连接成功，无法进行测速测试")
	}
	
	fmt.Printf("\n📊 连接结果: %d/%d 节点连接成功\n", connectedCount, len(config.Nodes))
	
	// 显示连接详细信息
	fmt.Println("\n📡 连接详细信息:")
	speedTester.mutex.RLock()
	for nodeName, conn := range speedTester.Connections {
		if conn.IsConnected {
			fmt.Printf("   ✅ %s: 已连接 (区块 #%d)\n", nodeName, speedTester.Nodes[nodeName].LastBlockNum)
		} else {
			fmt.Printf("   ❌ %s: 连接失败\n", nodeName)
		}
	}
	speedTester.mutex.RUnlock()
	
	// 开始完整的测速测试
	speedTester.StartSpeedTest()
	
	// 生成最终详细报告
	if speedTester.Config.Output.ShowFinalSummary {
		speedTester.GenerateFinalReport()
	}
	
	// 断开所有连接
	speedTester.DisconnectAllNodes()
	
	// TODO: 后续步骤将在这里实现
	// 步骤7: 输出报告（文件输出、JSON格式等）
	// 步骤8: 测试优化（重连机制、性能优化等）
	
	fmt.Println("\n✅ 步骤6完成: 统计功能实现和测试!")
	log.Println("等待步骤7: 输出报告优化...")
}