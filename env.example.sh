#!/bin/bash
# 开发环境配置模板：复制为 env.sh 并按本机实际路径修改（env.sh 不入库，见 .gitignore）
# 按本机实际路径调整（示例为 ~/go）
export GOROOT="$HOME/go"
export GOPATH="$HOME/go-path"
export PATH=$GOROOT/bin:$GOPATH/bin:$PATH
