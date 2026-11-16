#!/bin/bash
# 开发环境配置模板：复制为 env.sh 并按本机实际路径修改（env.sh 不入库，见 .gitignore）
export GOROOT=/home/test/go
export GOPATH=/home/test/go-path
export PATH=$GOROOT/bin:$GOPATH/bin:$PATH
