package cache

import (
	"errors"
	"github.com/karlseguin/ccache"
	"time"
)

var (
	cache = ccache.New(ccache.Configure().MaxSize(1000).ItemsToPrune(100))

	cacheConvertError=errors.New("convert result to string failed")
)


func PutExecuteError(txid string,data string){
	cache.Set(txid,data,time.Minute * 10)
}

func GetExecuteError(txid string)(string,error){

	value:=cache.Get(txid)
	if value ==nil || value.Value()==nil{
		return "",nil
	}
	t,ok:= value.Value().(string)
	if !ok{
		return "",cacheConvertError
	}
	return t,nil
}


