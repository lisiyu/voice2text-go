//go:build windows

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// genPNGIcon 程序化绘制一个麦克风图标（64x64 PNG，透明背景）
// 设计：白色圆角胶囊（麦克风头）+ 蓝色格栅线 + 蓝色 U 型支架 + 立杆
func genPNGIcon() []byte {
	const S = 64
	img := image.NewRGBA(image.Rect(0, 0, S, S))
	blue := color.RGBA{0x2A, 0x6E, 0xD2, 0xFF}
	light := color.RGBA{0xEA, 0xF2, 0xFC, 0xFF}

	for y := 0; y < S; y++ {
		for x := 0; x < S; x++ {
			// 1) 麦克风胶囊（圆角矩形）(24,12)-(40,30) r=4
			if inRoundedRect(x, y, 24, 12, 40, 30, 4) {
				img.Set(x, y, light)
			}
			// 2) 胶囊内格栅线（蓝色横线）
			if inRect(x, y, 26, 18, 38, 19) || inRect(x, y, 26, 22, 38, 23) || inRect(x, y, 26, 26, 38, 27) {
				img.Set(x, y, blue)
			}
			// 3) U 型支架（左右竖杆 + 底弧）
			if inRect(x, y, 20, 28, 24, 41) || inRect(x, y, 40, 28, 44, 41) || inRect(x, y, 20, 41, 44, 44) {
				img.Set(x, y, blue)
			}
			// 4) 立杆
			if inRect(x, y, 30, 44, 34, 56) {
				img.Set(x, y, blue)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// genICOIcon 把 genPNGIcon 的 PNG 包进 ICO 容器（Win10+ 支持 PNG-in-ICO），
// 供 LoadImageW(LR_LOADFROMFILE) 加载为 HICON 用作托盘图标。
func genICOIcon() []byte {
	png := genPNGIcon()
	var ico []byte
	// ICONDIR: idReserved(0), idType(1=ICO), idCount(1)
	ico = append(ico, 0, 0, 1, 0, 1, 0)
	// ICONDIRENTRY (16 字节)
	ico = append(ico, 64)    // bWidth
	ico = append(ico, 64)    // bHeight
	ico = append(ico, 0)     // bColorCount (0=8bpp+)
	ico = append(ico, 0)     // bReserved
	ico = append(ico, 1, 0)  // wPlanes = 1
	ico = append(ico, 32, 0) // wBitCount = 32（含 alpha）
	n := uint32(len(png))    // dwBytesInRes
	ico = append(ico, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
	off := uint32(6 + 16) // dwImageOffset（紧跟头部+1条目）
	ico = append(ico, byte(off), byte(off>>8), byte(off>>16), byte(off>>24))
	// 图像数据
	ico = append(ico, png...)
	return ico
}

func inRect(x, y, x1, y1, x2, y2 int) bool {
	return x >= x1 && x < x2 && y >= y1 && y < y2
}

// inRoundedRect 圆角矩形命中测试
func inRoundedRect(x, y, x1, y1, x2, y2, r int) bool {
	if x < x1 || x >= x2 || y < y1 || y >= y2 {
		return false
	}
	// 四个圆角区域
	cx, cy := 0, 0
	switch {
	case x < x1+r && y < y1+r:
		cx, cy = x1+r, y1+r
	case x >= x2-r && y < y1+r:
		cx, cy = x2-r-1, y1+r
	case x < x1+r && y >= y2-r:
		cx, cy = x1+r, y2-r-1
	case x >= x2-r && y >= y2-r:
		cx, cy = x2-r-1, y2-r-1
	default:
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}
