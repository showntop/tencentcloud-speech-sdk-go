package tts

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/google/uuid"
	"github.com/showntop/tencentcloud-speech-sdk-go/common"
)

// SpeechWsv2SynthesisResponse response
type SpeechWsv2SynthesisResponse struct {
	SessionId string               `json:"session_id"` //音频流唯一 id，由客户端在握手阶段生成并赋值在调用参数中
	RequestId string               `json:"request_id"` //音频流唯一 id，由服务端在握手阶段自动生成
	MessageId string               `json:"message_id"` //本 message 唯一 id
	Code      int                  `json:"code"`       //状态码，0代表正常，非0值表示发生错误
	Message   string               `json:"message"`    //错误说明，发生错误时显示这个错误发生的具体原因，随着业务发展或体验优化，此文本可能会经常保持变更或更新
	Result    Synthesisv2Subtitles `json:"result"`     //最新语音合成文本结果
	Final     int                  `json:"final"`      //该字段返回1时表示文本全部合成结束，客户端收到后需主动关闭 websocket 连接
	Ready     int                  `json:"ready"`      //该字段返回1时表示文本全部合成结束，客户端收到后需主动关闭 websocket 连接
	Heartbeat int                  `json:"heartbeat"`  //该字段返回1时表示文本全部合成结束，客户端收到后需主动关闭 websocket 连接
}

func (s *SpeechWsv2SynthesisResponse) ToString() string {
	d, _ := json.Marshal(s)
	return string(d)
}

// Synthesisv2Subtitles subtitles
type Synthesisv2Subtitles struct {
	Subtitles []Synthesisv2Subtitle `json:"subtitles"`
}

// Synthesisv2Subtitle  Subtitle
type Synthesisv2Subtitle struct {
	Text       string
	Phoneme    string
	BeginTime  int64
	EndTime    int64
	BeginIndex int
	EndIndex   int
}

// SpeechWsv2Synthesizer is the entry for TTS websocket service
type SpeechWsv2Synthesizer struct {
	Credential       *common.Credential
	action           string  `json:"Action"`
	AppID            int64   `json:"AppId"`
	Timestamp        int64   `json:"Timestamp"`
	Expired          int64   `json:"Expired"`
	SessionId        string  `json:"SessionId"`
	Text             string  `json:"Text"`
	ModelType        int64   `json:"ModelType"`
	VoiceType        int64   `json:"VoiceType"`
	SampleRate       int64   `json:"SampleRate"`
	Codec            string  `json:"Codec"`
	Speed            float64 `json:"Speed"`
	Volume           float64 `json:"Volume"`
	EnableSubtitle   bool    `json:"EnableSubtitle"`
	EmotionCategory  string  `json:"EmotionCategory"`
	EmotionIntensity int64   `json:"EmotionIntensity"`
	SegmentRate      int64   `json:"SegmentRate"`
	ExtParam         map[string]string

	ProxyURL    string
	mutex       sync.Mutex
	receiveEnd  chan int
	eventChan   chan speechWsSynthesisEventv2
	eventEnd    chan int
	listener    SpeechWsv2SynthesisListener
	status      int
	statusMutex sync.Mutex
	conn        *websocket.Conn //for websocet connection
	started     bool

	Debug     bool //是否debug
	DebugFunc func(message string)
}

// SpeechWsv2SynthesisListener is the listener of
type SpeechWsv2SynthesisListener interface {
	OnSynthesisStart(*SpeechWsv2SynthesisResponse)
	OnSynthesisEnd(*SpeechWsv2SynthesisResponse)
	OnAudioResult(data []byte)
	OnTextResult(*SpeechWsv2SynthesisResponse)
	OnSynthesisFail(*SpeechWsv2SynthesisResponse, error)
}

const (
	defaultWsVoiceTypev2  = 0
	defaultWsSampleRatev2 = 16000
	defaultWsCodecv2      = "pcm"
	defaultWsActionv2     = "TextToStreamAudioWSv2"
	wsConnectTimeoutv2    = 2000
	wsReadHeaderTimeoutv2 = 2000
	maxWsMessageSizev2    = 10240
	wsProtocolv2          = "wss"
	wsHostv2              = "tts.cloud.tencent.com"
	wsPathv2              = "/stream_wsv2"
)

const (
	eventTypeWsStartv2 = iota
	eventTypeWsEndv2
	eventTypeWsAudioResultv2
	eventTypeWsTextResultv2
	eventTypeWsFailv2
)

type eventWsTypev2 int

type speechWsSynthesisEventv2 struct {
	t   eventWsTypev2
	r   *SpeechWsv2SynthesisResponse
	d   []byte
	err error
}

// NewSpeechWsv2Synthesizer creates instance of SpeechWsv2Synthesizer
func NewSpeechWsv2Synthesizer(appID int64, credential *common.Credential, listener SpeechWsv2SynthesisListener) *SpeechWsv2Synthesizer {
	return &SpeechWsv2Synthesizer{
		AppID:      appID,
		Credential: credential,
		action:     defaultWsActionv2,
		VoiceType:  defaultWsVoiceTypev2,
		SampleRate: defaultWsSampleRatev2,
		Codec:      defaultWsCodecv2,
		listener:   listener,
		status:     0,
		receiveEnd: make(chan int),
		eventChan:  make(chan speechWsSynthesisEventv2, 10),
		eventEnd:   make(chan int),
	}
}

// Synthesis Start connects to server and start a synthesizer session
func (synthesizer *SpeechWsv2Synthesizer) Prepare() error {
	synthesizer.mutex.Lock()
	defer synthesizer.mutex.Unlock()

	if synthesizer.started {
		return fmt.Errorf("synthesizer is already started")
	}
	if synthesizer.SessionId == "" {
		SessionId := uuid.New().String()
		synthesizer.SessionId = SessionId
	}
	var timestamp = time.Now().Unix()
	synthesizer.Timestamp = timestamp
	synthesizer.Expired = timestamp + 24*60*60
	serverURL := synthesizer.buildURL(false)
	signature := synthesizer.genWsSignature(serverURL, synthesizer.Credential.SecretKey)
	if synthesizer.Debug && synthesizer.DebugFunc != nil {
		logMsg := fmt.Sprintf("serverURL:%s , signature:%s", serverURL, signature)
		synthesizer.DebugFunc(logMsg)
	}
	dialer := websocket.Dialer{}
	if len(synthesizer.ProxyURL) > 0 {
		proxyURL, _ := url.Parse(synthesizer.ProxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}
	serverURL = synthesizer.buildURL(true)
	header := http.Header(make(map[string][]string))
	urlStr := fmt.Sprintf("%s://%s&Signature=%s", wsProtocolv2, serverURL, url.QueryEscape(signature))
	if synthesizer.Debug && synthesizer.DebugFunc != nil {
		logMsg := fmt.Sprintf("urlStr:%s ", urlStr)
		synthesizer.DebugFunc(logMsg)
	}
	conn, _, err := dialer.Dial(urlStr, header)
	if err != nil {
		return fmt.Errorf("session_id: %s, error: %s", synthesizer.SessionId, err.Error())
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("session_id: %s, error: %s", synthesizer.SessionId, err.Error())
	}
	msg := SpeechWsv2SynthesisResponse{}
	err = json.Unmarshal(data, &msg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("session_id: %s, error: %s", synthesizer.SessionId, err.Error())
	}
	if msg.Code != 0 {
		conn.Close()
		return fmt.Errorf("session_id: %s, code: %d, message: %s",
			synthesizer.SessionId, msg.Code, msg.Message)
	}
	msg.SessionId = synthesizer.SessionId
	synthesizer.conn = conn
	// wait ready
	for {
		optCode, data, err := synthesizer.conn.ReadMessage()
		if err != nil {
			conn.Close()
			return err
		}
		if optCode == websocket.TextMessage {
			if msg.Code != 0 {
				conn.Close()
				return fmt.Errorf(msg.Message)
			}
		}
		msg2 := SpeechWsv2SynthesisResponse{}
		if err2 := json.Unmarshal(data, &msg2); err2 != nil {
			return err
		}
		if msg2.Ready == 1 {
			break
		}
	}
	// send
	go synthesizer.receive()
	go synthesizer.eventDispatch()
	synthesizer.started = true
	synthesizer.setStatus(eventTypeWsStartv2)
	synthesizer.eventChan <- speechWsSynthesisEventv2{
		t:   eventTypeWsStartv2,
		r:   &msg,
		err: nil,
	}
	return nil
}

func (synthesizer *SpeechWsv2Synthesizer) Send(chunk string) error {
	return synthesizer.conn.WriteJSON(map[string]interface{}{
		"session_id": synthesizer.SessionId,
		"message_id": uuid.New().String(),
		"action":     "ACTION_SYNTHESIS",
		"data":       chunk,
	})
}

func (synthesizer *SpeechWsv2Synthesizer) Complete() error {
	return synthesizer.conn.WriteJSON(map[string]interface{}{
		"session_id": synthesizer.SessionId,
		"message_id": uuid.New().String(),
		"action":     "ACTION_COMPLETE",
		"data":       "",
	})
}

func (synthesizer *SpeechWsv2Synthesizer) receive() {
	defer func() {
		// handle panic
		synthesizer.genRecoverFunc()()
		close(synthesizer.eventChan)
		close(synthesizer.receiveEnd)
	}()
	for {
		optCode, data, err := synthesizer.conn.ReadMessage()
		if err != nil {
			synthesizer.onError(fmt.Errorf("SessionId: %s, error: %s", synthesizer.SessionId, err.Error()))
			break
		}
		if optCode == websocket.BinaryMessage {
			msg := SpeechWsv2SynthesisResponse{SessionId: synthesizer.SessionId}
			synthesizer.eventChan <- speechWsSynthesisEventv2{
				t:   eventTypeWsAudioResultv2,
				r:   &msg,
				d:   data,
				err: nil,
			}
		}
		if optCode == websocket.TextMessage {
			if synthesizer.Debug && synthesizer.DebugFunc != nil {
				synthesizer.DebugFunc(string(data))
			}
			msg := SpeechWsv2SynthesisResponse{}
			err = json.Unmarshal(data, &msg)
			if err != nil {
				synthesizer.onError(fmt.Errorf("SessionId: %s, error: %s",
					synthesizer.SessionId, err.Error()))
				break
			}
			msg.SessionId = synthesizer.SessionId
			if msg.Code != 0 {
				synthesizer.onError(fmt.Errorf("VoiceID: %s, error code %d, message: %s",
					synthesizer.SessionId, msg.Code, msg.Message))
				break
			}
			if msg.Final == 1 {
				synthesizer.setStatus(eventTypeWsEndv2)
				synthesizer.closeConn()
				synthesizer.eventChan <- speechWsSynthesisEventv2{
					t:   eventTypeWsEndv2,
					r:   &msg,
					err: nil,
				}
				break
			}
			synthesizer.eventChan <- speechWsSynthesisEventv2{
				t:   eventTypeWsTextResultv2,
				r:   &msg,
				err: nil,
			}
		}
	}
}

func (synthesizer *SpeechWsv2Synthesizer) eventDispatch() {
	defer func() {
		// handle panic
		synthesizer.genRecoverFunc()()
		close(synthesizer.eventEnd)
	}()
	for e := range synthesizer.eventChan {
		switch e.t {
		case eventTypeWsStartv2:
			synthesizer.listener.OnSynthesisStart(e.r)
		case eventTypeWsEndv2:
			synthesizer.listener.OnSynthesisEnd(e.r)
		case eventTypeWsAudioResultv2:
			synthesizer.listener.OnAudioResult(e.d)
		case eventTypeWsTextResultv2:
			synthesizer.listener.OnTextResult(e.r)
		case eventTypeWsFailv2:
			synthesizer.listener.OnSynthesisFail(e.r, e.err)
		}
	}
}

// Wait Wait
func (synthesizer *SpeechWsv2Synthesizer) Wait() error {
	synthesizer.mutex.Lock()
	defer synthesizer.mutex.Unlock()
	<-synthesizer.eventEnd
	<-synthesizer.receiveEnd
	return nil
}

func (synthesizer *SpeechWsv2Synthesizer) getStatus() int {
	synthesizer.statusMutex.Lock()
	defer synthesizer.statusMutex.Unlock()
	status := synthesizer.status
	return status
}

func (synthesizer *SpeechWsv2Synthesizer) setStatus(status int) {
	synthesizer.statusMutex.Lock()
	defer synthesizer.statusMutex.Unlock()
	synthesizer.status = status
}

func (synthesizer *SpeechWsv2Synthesizer) onError(err error) {
	r := &SpeechWsv2SynthesisResponse{
		SessionId: synthesizer.SessionId,
	}
	synthesizer.closeConn()
	synthesizer.eventChan <- speechWsSynthesisEventv2{
		t:   eventTypeWsFailv2,
		r:   r,
		err: err,
	}
}

func (synthesizer *SpeechWsv2Synthesizer) buildURL(escape bool) string {
	var queryMap = make(map[string]string)
	queryMap["Action"] = synthesizer.action
	queryMap["AppId"] = strconv.FormatInt(synthesizer.AppID, 10)
	queryMap["SecretId"] = synthesizer.Credential.SecretId
	queryMap["Timestamp"] = strconv.FormatInt(synthesizer.Timestamp, 10)
	queryMap["Expired"] = strconv.FormatInt(synthesizer.Expired, 10)
	if escape {
		//url escapes the string so it can be safely placed
		queryMap["Text"] = url.QueryEscape(synthesizer.Text)
	} else {
		queryMap["Text"] = synthesizer.Text
	}
	queryMap["SessionId"] = synthesizer.SessionId
	queryMap["ModelType"] = strconv.FormatInt(synthesizer.ModelType, 10)
	queryMap["VoiceType"] = strconv.FormatInt(synthesizer.VoiceType, 10)
	queryMap["SampleRate"] = strconv.FormatInt(synthesizer.SampleRate, 10)
	queryMap["Speed"] = strconv.FormatFloat(synthesizer.Speed, 'g', -1, 64)
	queryMap["Volume"] = strconv.FormatFloat(synthesizer.Volume, 'g', -1, 64)
	queryMap["Codec"] = synthesizer.Codec
	queryMap["EnableSubtitle"] = strconv.FormatBool(synthesizer.EnableSubtitle)
	queryMap["EmotionCategory"] = synthesizer.EmotionCategory
	queryMap["EmotionIntensity"] = strconv.FormatInt(synthesizer.EmotionIntensity, 10)
	queryMap["SegmentRate"] = strconv.FormatInt(synthesizer.SegmentRate, 10)
	for k, v := range synthesizer.ExtParam {
		queryMap[k] = v
	}
	var keys []string
	for k := range queryMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var queryStrBuffer bytes.Buffer
	for _, k := range keys {
		queryStrBuffer.WriteString(k)
		queryStrBuffer.WriteString("=")
		queryStrBuffer.WriteString(queryMap[k])
		queryStrBuffer.WriteString("&")
	}
	rs := []rune(queryStrBuffer.String())
	rsLen := len(rs)
	queryStr := string(rs[0 : rsLen-1])
	serverURL := fmt.Sprintf("%s%s", wsHostv2, wsPathv2)
	signURL := fmt.Sprintf("%s?%s", serverURL, queryStr)
	return signURL
}

func (synthesizer *SpeechWsv2Synthesizer) genWsSignature(signURL string, secretKey string) string {
	hmac := hmac.New(sha1.New, []byte(secretKey))
	signURL = "GET" + signURL
	hmac.Write([]byte(signURL))
	encryptedStr := hmac.Sum([]byte(nil))
	return base64.StdEncoding.EncodeToString(encryptedStr)
}

func (synthesizer *SpeechWsv2Synthesizer) genRecoverFunc() func() {
	return func() {
		if r := recover(); r != nil {
			var err error
			switch r := r.(type) {
			case error:
				err = r
			default:
				err = fmt.Errorf("%v", r)
			}
			retErr := fmt.Errorf("panic error ocurred! [err: %s] [stack: %s]",
				err.Error(), string(debug.Stack()))
			msg := SpeechWsv2SynthesisResponse{
				SessionId: synthesizer.SessionId,
			}
			synthesizer.eventChan <- speechWsSynthesisEventv2{
				t:   eventTypeWsFailv2,
				r:   &msg,
				err: retErr,
			}
		}
	}
}

// CloseConn close connection
func (synthesizer *SpeechWsv2Synthesizer) CloseConn() {
	synthesizer.closeConn()
}

func (synthesizer *SpeechWsv2Synthesizer) closeConn() {
	err := synthesizer.conn.Close()
	if err != nil && synthesizer.Debug && synthesizer.DebugFunc != nil {
		synthesizer.DebugFunc(fmt.Sprintf("%s %s", time.Now().String(), err.Error()))
	}
}
