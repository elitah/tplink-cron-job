package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/elitah/utils/wait"

	"github.com/robfig/cron/v3"
)

const (
	shortToken = "RDpbLfCPsJZ7fiv"
	longToken  = "yLwVl0zKqws7LgKPRQ84Mdt708T1qQ3Ha7xv3H7NyU84p21BriUWBU43odz3iP4rBL3cD02KZciXTysVXiV8ngg6vL48rPJyAUw0HurW20xqxv9aYb4M9wK1Ae0wlro510qXeU07kV57fQMc8L6aLgMLwygtc0F10a0Dg70TOoouyFhdysuRMO51yY5ZlOZZLEal1h0t9YQW0Ko7oBwmCAHoic4HYbUyVeU3sfQ1xtXcPcf1aT303wAQhv66qzW"
)

var (
	GHTTPLimit = make(chan byte, 1)

	GCron = cron.New(cron.WithSeconds())
)

type SpeedLimitValue struct {
	name string

	next time.Time

	upList   [5]int64
	downList [5]int64

	upIndex   int
	downIndex int

	upLimit   int64
	downLimit int64
}

func (this *SpeedLimitValue) Update(name string, up, down, up_limit, down_limit int64) {
	this.name = name

	this.upList[this.upIndex] = up
	this.downList[this.downIndex] = down

	this.upIndex++
	this.downIndex++

	if len(this.upList) <= this.upIndex {
		this.upIndex = 0
	}

	if len(this.downList) <= this.downIndex {
		this.downIndex = 0
	}

	this.upLimit = up_limit
	this.downLimit = down_limit
}

func (this *SpeedLimitValue) Avg(up bool) (result int64) {
	//
	avg := true
	//
	list := this.upList[:]
	//
	if 0 < this.upLimit {
		//
		avg = false
	}
	//
	if !up {
		//
		list = this.downList[:]
		//
		if 0 < this.downLimit {
			//
			avg = false
		} else {
			//
			avg = true
		}
	}
	//
	n := int64(len(list))
	//
	for _, item := range list {
		//
		if avg {
			result += item
		} else if result < item {
			result = item
		}
	}
	//
	if avg {
		result /= n
	}
	//
	return
}

type SpeedLimit struct {
	up   int64
	down int64

	m map[string]*SpeedLimitValue
}

func (this *SpeedLimit) Checking(fn func(string, string, int64, int64)) {
	//
	var up, down int64
	//
	nowtime := time.Now()
	//
	for k, v := range this.m {
		//
		if nowtime.After(v.next) {
			//
			_up := v.Avg(true)
			_down := v.Avg(false)
			//
			if this.up < _up {
				up = this.up / 1024
			} else if int64(float32(this.up)*0.6) > _up {
				up = 0
			} else {
				up = v.upLimit
			}
			//
			if this.down < _down {
				down = this.down / 1024
			} else if int64(float32(this.down)*0.6) > _down {
				down = 0
			} else {
				down = v.downLimit
			}
			//
			if (up != v.upLimit) || (down != v.downLimit) {
				//
				fn(k, v.name, up, down)
				//
				fmt.Printf("\033[31;1m%s\033[0m: Set to %6d/%6d KB/s, Avg: %6d/%6d KB/s, Limit: %6d/%6d KB/s\n", k, up, down, _up/1024, _down/1024, v.upLimit, v.downLimit)
			}
			//
			v.next = nowtime.Add(3 * time.Second)
		}
	}
}

func (this *SpeedLimit) Update(mac, name string, up, down, up_limit, down_limit int64) {
	//
	//fmt.Println(mac)
	//
	if "" != mac {
		//
		v, ok := this.m[mac]
		//
		if !ok {
			//
			v = &SpeedLimitValue{}
			//
			this.m[mac] = v
		}
		//
		v.Update(name, up, down, up_limit, down_limit)
	}
}

func (this *SpeedLimit) String() string {
	//
	var s strings.Builder
	//
	fmt.Fprintln(
		&s,
		"====================================================================",
	)
	//
	for k, v := range this.m {
		//
		v0 := v.Avg(true)
		v1 := v.Avg(false)
		//
		if this.up < v0 || this.down < v1 {
			//
			fmt.Fprintf(
				&s,
				"%s: Up: %d(%v %v), Down: %d(%v %v)\n",
				k,
				v0, v.upList[v.upIndex:], v.upList[:v.upIndex],
				v1, v.downList[v.downIndex:], v.downList[:v.downIndex],
			)
		}
	}
	//
	return s.String()
}

type WANInfo struct {
	IpAddr     string `json:"ipaddr"`
	Netmask    string `json:"netmask"`
	Gateway    string `json:"gateway"`
	PriDns     string `json:"pri_dns"`
	SndDns     string `json:"snd_dns"`
	LinkStatus int    `json:"link_status"`
	ErrorCode  int    `json:"error_code"`
	Proto      string `json:"proto"`
	UpTime     int64  `json:"up_time"`
	UpSpeed    int64  `json:"up_speed"`
	DownSpeed  int64  `json:"down_speed"`
	PhyStatus  int    `json:"phy_status"`
}

type HostInfo struct {
	Mac       string            `json:"mac"`
	Type      int               `json:"type,string"`
	Blocked   int               `json:"blocked,string"`
	IP        string            `json:"ip"`
	HostName  string            `json:"hostname"`
	UpSpeed   int64             `json:"up_speed,string"`
	DownSpeed int64             `json:"down_speed,string"`
	UpLimit   int64             `json:"up_limit,string"`
	DownLimit int64             `json:"down_limit,string"`
	IsCurHost int               `json:"is_cur_host,string"`
	SSID      string            `json:"ssid"`
	WifiMode  int               `json:"wifi_mode,string"`
	PlanRule  []json.RawMessage `json:"plan_rule"`
}

func securityEncode(password string) (result string) {
	if "" != password {
		var limitLength int

		passLenth := len(password)
		shortLength := len(shortToken)
		longLength := len(longToken)

		if passLenth > shortLength {
			limitLength = passLenth
		} else {
			limitLength = shortLength
		}

		for i := 0; limitLength > i; i++ {
			n1 := 187
			n2 := 187

			if passLenth <= i {
				n1 = int(shortToken[i])
			} else if shortLength <= i {
				n2 = int(password[i])
			} else {
				n1 = int(shortToken[i])
				n2 = int(password[i])
			}

			result += string(longToken[(n1^n2)%longLength])
		}
	}

	return
}

func httpResponse(url, contentType string, body io.Reader, jbody interface{}, nocheck ...bool) error {
	//
	GHTTPLimit <- 1
	//
	defer func() {
		<-GHTTPLimit
	}()
	//
	if _body, ok := body.(interface {
		Reload()
	}); ok {
		_body.Reload()
	}
	//
	if _body, ok := body.(interface {
		Seek(int64, int) (int64, error)
	}); ok {
		_body.Seek(io.SeekStart, 0)
	}
	//
	if resp, err := http.Post(
		url,
		contentType,
		body,
	); nil == err {
		//
		defer resp.Body.Close()
		//
		if http.StatusOK == resp.StatusCode || (0 < len(nocheck) && nocheck[0]) {
			if nil != jbody {
				//
				if data, err := ioutil.ReadAll(resp.Body); nil == err {
					//
					return json.Unmarshal(data, jbody)
				} else {
					return err
				}
			} else {
				return nil
			}
		} else {
			if http.StatusUnauthorized == resp.StatusCode {
				return syscall.EACCES
			} else {
				return fmt.Errorf("error with http status code: %d(%s)\n", resp.StatusCode, http.StatusText(resp.StatusCode))
			}
		}
	} else {
		return err
	}
}

func init() {
	GCron.Start()
}

func main() {
	var debug bool

	var address string
	var password string

	var httpaddr string

	var reboot string

	var up int
	var down int

	flag.BoolVar(&debug, "d", false, "debug flag")

	flag.StringVar(&address, "ip", "", "your tplink router's ip address")
	flag.StringVar(&password, "psk", "", "your tplink router's login password")

	flag.StringVar(&httpaddr, "http", "", "your local http server")

	flag.StringVar(&reboot, "reboot", "", "your tplink router's reboot time, eg: 3:15:15")

	flag.IntVar(&up, "up", 0, "limit up")
	flag.IntVar(&down, "down", 0, "limit down")

	flag.Parse()

	if "" != address && "" != password {
		//
		var mu sync.RWMutex
		//
		var hostsList []*HostInfo
		//
		var wanInfo *WANInfo
		//
		ctlchan := make(chan string, 1)
		//
		defer close(ctlchan)
		//
		go func() {
			//
			var speedLimit *SpeedLimit
			//
			loginFailed := 0
			//
			dsUrl := ""
			//
			login_request := strings.NewReader(
				fmt.Sprintf(
					`{"method":"do","login":{"password":"%s"}}`,
					securityEncode(password),
				),
			)
			//
			network_request := strings.NewReader(
				`{"method":"get","network":{"name":["wan_status","lan_status"]},"hosts_info":{"table":"online_host"}}`,
			)
			//
			reboot_request := strings.NewReader(
				`{"method":"do","system":{"reboot":null}}`,
			)
			//
			rebootFn := func() {
				//
				retry := 0
				//
				start := time.Now()
				//
				for 5 > retry {
					//
					if 30*time.Minute < time.Since(start) {
						//
						fmt.Println("timeout execute")
						//
						return
					}
					//
					if "" != dsUrl {
						if err := httpResponse(
							dsUrl,
							"application/json; charset=UTF-8",
							reboot_request,
							nil,
						); nil == err {
							return
						} else if errors.Is(err, syscall.EACCES) {
							//
							retry++
							//
							time.Sleep(10 * time.Second)
							//
							continue
						}
					}
					//
					time.Sleep(3 * time.Second)
				}
			}
			//
			if "" != reboot {
				if t, err := time.Parse("15:04:05", reboot); nil == err {
					//
					h, m, s := t.Clock()
					//
					fmt.Printf(`=======================================
	Reboot at: %d:%d:%d
=======================================


`,
						h,
						m,
						s,
					)
					//
					reboot = fmt.Sprintf(
						"%d %d %d * * *",
						s,
						m,
						h,
					)
					//
					if _, err := GCron.AddFunc(
						reboot,
						func() {
							go rebootFn()
						},
					); nil != err {
						fmt.Println(err)
					}
				} else {
					fmt.Println(err)
				}
			}
			//
			go func() {
				//
				cooldown := 0
				//
				for {
					//
					if 5 <= loginFailed {
						//
						cooldown++
						//
						if 7200 < cooldown {
							loginFailed = 0
						}
					} else {
						//
						cooldown = 0
					}
					//
					time.Sleep(time.Second)
				}
			}()
			//
			go func() {
				//
				for {
					select {
					case msg, ok := <-ctlchan:
						//
						if ok {
							//
							switch msg {
							case "reboot":
								go rebootFn()
							}
						} else {
							return
						}
					}
				}
			}()
			//
			if 0 < up && 0 < down {
				//
				speedLimit = &SpeedLimit{
					up:   int64(up) * 1024,   // 上传
					down: int64(down) * 1024, // 下载

					m: make(map[string]*SpeedLimitValue),
				}
			}
			//
			for {
				//
				if "" == dsUrl && 5 > loginFailed {
					//
					result := struct {
						ErrorCode int    `json:"error_code"`
						Stok      string `json:"stok"`
					}{}
					//
					if err := httpResponse(
						fmt.Sprintf(
							"http://%s/",
							address,
						),
						"application/json; charset=UTF-8",
						login_request,
						&result,
					); nil == err {
						//
						if "" != result.Stok {
							//
							loginFailed = 0
							//
							dsUrl = fmt.Sprintf("http://%s/stok=%s/ds", address, result.Stok)
						} else {
							fmt.Printf("invalid stok: %s\n", result.Stok)
						}
					} else {
						//
						if errors.Is(err, syscall.EACCES) {
							loginFailed++
						}
						//
						fmt.Printf("http request failed: %v\n", err)
					}
				}

				if "" != dsUrl {
					//
					//fmt.Println(dsUrl)
					//
					result := struct {
						ErrorCode int `json:"error_code"`
						Network   struct {
							WanStatus *WANInfo `json:"wan_status"`
							LanStatus map[string]struct {
								PhyStatus int `json:"phy_status,string"`
							} `json:"lan_status"`
						} `json:"network"`
						HostsInfo struct {
							OnlineHost []map[string]*HostInfo `json:"online_host"`
						} `json:"hosts_info"`
					}{}
					//
					if err := httpResponse(
						dsUrl,
						"application/json; charset=UTF-8",
						network_request,
						&result,
					); nil == err {
						//
						mu.Lock()
						//
						wanInfo = result.Network.WanStatus
						//
						if n := len(result.HostsInfo.OnlineHost); 0 < n {
							//
							hostsList = make([]*HostInfo, 0, n)
							//
							for _, item := range result.HostsInfo.OnlineHost {
								//
								for _, _item := range item {
									//
									hostsList = append(hostsList, _item)
									//
									if nil != speedLimit {
										speedLimit.Update(_item.Mac, _item.HostName, _item.UpSpeed, _item.DownSpeed, _item.UpLimit, _item.DownLimit)
									}
								}
							}
						}
						//
						if nil != speedLimit {
							//
							speedLimit.Checking(func(mac, name string, up, down int64) {
								//
								//fmt.Println(mac, up, down)
								//
								go func(url, mac, name string, up, down int64) {
									//
									httpResponse(
										url,
										"application/json; charset=UTF-8",
										strings.NewReader(
											fmt.Sprintf(
												`{"method":"do","hosts_info":{"set_block_flag":{"mac":"%s","is_blocked":"0","name":"%s","up_limit":%d,"down_limit":"%d"}}}`,
												mac,
												name,
												up,
												down,
											),
										),
										nil,
									)
								}(dsUrl, mac, name, up, down)
							})
						}
						//
						mu.Unlock()
						//
						if debug {
							//
							fmt.Println("==============================================")
							//
							fmt.Print(
								"LAN Status:",
							)
							//
							for _, item := range result.Network.LanStatus {
								fmt.Printf(
									"-%d",
									item.PhyStatus,
								)
							}
							//
							fmt.Println()
							//
							fmt.Printf(
								`WAN Status:
	IP: %s, Netmast: %s, Gateway: %s
	DNS: %s %s
	PhyStatus: %d, LinkStatus: %d
	Proto: %s
	UpTime: %d
	Speed: %d KB/s %d KB/s
`,
								result.Network.WanStatus.IpAddr,
								result.Network.WanStatus.Netmask,
								result.Network.WanStatus.Gateway,
								result.Network.WanStatus.PriDns,
								result.Network.WanStatus.SndDns,
								result.Network.WanStatus.PhyStatus,
								result.Network.WanStatus.LinkStatus,
								result.Network.WanStatus.Proto,
								result.Network.WanStatus.UpTime,
								result.Network.WanStatus.UpSpeed,
								result.Network.WanStatus.DownSpeed,
							)
							//
							fmt.Println()
						}
					} else {
						if errors.Is(err, syscall.EACCES) {
							dsUrl = ""
						}
						//
						fmt.Printf("http request failed: %v\n", err)
					}
					//
					time.Sleep(1 * time.Second)
				} else {
					//
					time.Sleep(3 * time.Second)
				}
			}
		}()

		if "" != httpaddr {
			//
			qn := func(val string) string {
				//
				if result, err := url.QueryUnescape(val); nil == err {
					return result
				}
				//
				return val
			}
			// Trinomial
			tn := func(c bool, a1, a2 interface{}) interface{} {
				//
				if c {
					return a1
				}
				//
				return a2
			}
			// speed
			pn := func(bps int64) interface{} {
				if 1000000 > bps {
					return fmt.Sprintf("%.2f Kbps", float32(bps)/1000.0)
				} else if 1000000000 > bps {
					return fmt.Sprintf("%.2f Mbps", float32(bps)/1000000.0)
				} else {
					return fmt.Sprintf("%.2f Gbps", float32(bps)/1000000000.0)
				}
			}
			//
			fn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				//
				switch r.URL.Path {
				case "/reboot":
					//
					select {
					case <-ctlchan:
					default:
					}
					//
					ctlchan <- "reboot"
					//
					w.Header().Set("Location", "/")
					//
					w.WriteHeader(http.StatusFound)
					//
					return
				}
				//
				fmt.Fprintf(
					w,
					`<!DOCTYPE html>
<html lang="zh-cmn-Hans">
<head>
<meta charset="utf-8">
<title>tplink</title>
<meta name="viewport" content="width=device-width, initial-scale=1, user-scalable=0">
<style type="text/css">
p {
    margin: 0;
    padding: 0;
}
table {
    min-width: 75%%;
}
</style>
</head>
<body>`,
				)
				//
				if "" != reboot {

				}
				//
				mu.RLock()
				//
				if nil != wanInfo {
					//
					fmt.Fprintf(
						w,
						`
<table border="0">
	<tr>
		<td>物理接口状态</td>
		<td>%s</td>
		<td>状态</td>
		<td>%s</td>
		<td>重启cron</td>
		<td>%s</td>
	</tr>
	<tr>
		<td>连接协议</td>
		<td>%s</td>
		<td>连接时长</td>
		<td>%s</td>
		<td></td>
		<td><a href="/reboot">马上重启</a></td>
	</tr>
	<tr>
		<td>IP</td>
		<td>%s</td>
		<td>掩码</td>
		<td>%s</td>
		<td>网关</td>
		<td>%s</td>
	</tr>
	<tr>
		<td>DNS1</td>
		<td>%s</td>
		<td>DNS2</td>
		<td>%s</td>
		<td></td>
		<td></td>
	</tr>
	<tr>
		<td>上传</td>
		<td>%s</td>
		<td>下载</td>
		<td>%s</td>
		<td></td>
		<td></td>
	</tr>
</table>`,
						tn(
							0 != wanInfo.PhyStatus,
							"<span style=\"color: green;\">已连接</div>",
							"<span style=\"color: red;\">未连接</div>",
						),
						tn(
							0 != wanInfo.LinkStatus,
							"<span style=\"color: green;\">已连接</div>",
							"<span style=\"color: red;\">未连接</div>",
						),
						reboot,
						wanInfo.Proto,
						(time.Duration(wanInfo.UpTime) * time.Second).String(),
						wanInfo.IpAddr,
						wanInfo.Netmask,
						wanInfo.Gateway,
						wanInfo.PriDns,
						wanInfo.SndDns,
						pn(wanInfo.UpSpeed*8192),
						pn(wanInfo.DownSpeed*8192),
					)
				}
				//
				fmt.Fprintf(
					w,
					`
<div style="margin: 1em 0; width: 100%%; height: 2px; background-color: #ccc;"></div>
<table border="0">
	<tr>
		<th>MAC</th>
		<th>IP</th>
		<th>主机名</th>
		<th>网络速度(上传/下载)</th>
		<th>限速(上传/下载)</th>
		<th>SSID</th>
	</tr>
`,
				)

				for _, item := range hostsList {
					fmt.Fprintf(
						w,
						`	<tr style="background-color: %s;">
		<td>%s</td>
		<td>%s</td>
		<td>%s</td>
		<td><p>%s</p><p>%s</p></td>
		<td><p>%s</p><p>%s</p></td>
		<td>%s</td>
	</tr>
`,
						tn(0 != item.Type, "#7fffd4", "#ccc"),
						item.Mac,
						item.IP,
						qn(item.HostName),
						pn(item.UpSpeed*8),
						pn(item.DownSpeed*8),
						pn(item.UpLimit*8192),
						pn(item.DownLimit*8192),
						item.SSID,
					)
				}

				mu.RUnlock()

				fmt.Fprintf(
					w,
					`
</table>
</body>
</html>
`,
				)
			})
			//
			srv := http.Server{
				Addr:    httpaddr,
				Handler: fn,
				//TLSConfig:    nil,
				//TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
			}
			//
			srv.SetKeepAlivesEnabled(true)
			//
			fmt.Println(srv.ListenAndServe())
		}

		if err := wait.Signal(
			wait.WithNotify(func(s os.Signal) bool {
				fmt.Println(s)
				return true
			}),
			wait.WithSignal(syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM),
		); nil != err {
			fmt.Println(err)
		}
	} else {
		flag.Usage()
	}
}
