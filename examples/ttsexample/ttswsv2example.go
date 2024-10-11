package main

import (
	"flag"
	"fmt"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/tts"
)

type MySpeechWsv2SynthesisListener struct {
	SessionId string
	Data      []byte
	Index     int
}

func (l *MySpeechWsv2SynthesisListener) OnSynthesisStart(r *tts.SpeechWsv2SynthesisResponse) {
	fmt.Printf("%s|OnSynthesisStart,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}

func (l *MySpeechWsv2SynthesisListener) OnSynthesisEnd(r *tts.SpeechWsv2SynthesisResponse) {
	fileName := fmt.Sprintf("test.mp3")
	tts.WriteFile(path.Join("./", fileName), l.Data)
	fmt.Printf("%s|OnSynthesisEnd,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}
func (l *MySpeechWsv2SynthesisListener) OnAudioResult(data []byte) {
	fmt.Printf("%s|OnAudioResult,sessionId:%s index:%d\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, l.Index)
	l.Index = l.Index + 1
	l.Data = append(l.Data, data...)
}
func (l *MySpeechWsv2SynthesisListener) OnTextResult(r *tts.SpeechWsv2SynthesisResponse) {
	fmt.Printf("%s|OnTextResult,sessionId:%s response: %s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString())
}
func (l *MySpeechWsv2SynthesisListener) OnSynthesisFail(r *tts.SpeechWsv2SynthesisResponse, err error) {
	fmt.Printf("%s|OnSynthesisFail,sessionId:%s response: %s err:%s\n", time.Now().Format("2006-01-02 15:04:05"), l.SessionId, r.ToString(), err.Error())
}

func main() {
	var c = flag.Int("c", 1, "concurrency")
	flag.Parse()
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		fmt.Println("Main: Starting worker", i)
		wg.Add(1)
		go processWs(i, &wg)
	}

	fmt.Println("Main: Waiting for workers to finish")
	wg.Wait()
	fmt.Println("Main: Completed")

}

func processWs(id int, wg *sync.WaitGroup) {
	defer wg.Done()
	//在腾讯云控制台账号信息页面查看账号APPID，访问管理页面获取 SecretID 和 SecretKey 。
	secretID := "AKIDc7QULyhRzOwQEqEMhxDKCqGfvPkutiOu"
	secretKey := "ax74oMjvwlOsAF7mEUZTEoBJG1bx8Azb"
	AppID := 1252214184 //替换为自己的appid

	sessionId := fmt.Sprintf("%s_%s", strconv.Itoa(id), uuid.New().String())
	listener := &MySpeechWsv2SynthesisListener{Data: make([]byte, 0), SessionId: sessionId}
	credential := common.NewCredential(secretID, secretKey)
	synthesizer := tts.NewSpeechWsv2Synthesizer(int64(AppID), credential, listener)
	synthesizer.SessionId = sessionId
	synthesizer.VoiceType = 1001
	synthesizer.Codec = "mp3"
	// synthesizer.Text = "<speak>\n现状是各地的经济水平是<phoneme alphabet=\"py\" ph=\"cen1 ci1 bu4 qi2\">参差不齐</phoneme>的。需要缩小较弱地域和较强地域的<phoneme alphabet=\"py\" ph=\"cha1 ju4\">差距</phoneme>。要做好这个<phoneme alphabet=\"py\" ph=\"chai1 shi4\">差事</phoneme>可不容易啊。\n</speak>\n"
	synthesizer.EnableSubtitle = true
	//synthesizer.EmotionCategory = "happy"
	//synthesizer.EmotionIntensity = 200
	//synthesizer.Debug = true
	//synthesizer.DebugFunc = func(message string) { fmt.Println(message) }
	err := synthesizer.Prepare()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	synthesizer.Send("现状是各地的经济水平是")
	synthesizer.Send("参差不齐")
	synthesizer.Send("的。需要缩小较弱地域和较强地域的")
	synthesizer.Send("差距。")
	synthesizer.Wait()
}
