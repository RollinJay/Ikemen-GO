//go:build !kinc

package main

import (
	_ "embed" // Support for go:embed resources
	"encoding/binary"
	"fmt"
	"runtime"

	gl "github.com/fyne-io/gl-js"
	"golang.org/x/mobile/exp/f32"
)

// ------------------------------------------------------------------
// ShaderProgram

type ShaderProgram struct {
	// Program
	program gl.Program
	// Attribute locations (sprite shaders)
	aPos gl.Attrib
	aUv  gl.Attrib
	// Attribute locations (postprocess shaders)
	aVert gl.Attrib
	// Common uniforms used by most shaders
	uModelView  gl.Uniform
	uProjection gl.Uniform
	uTexture    gl.Uniform
	uAlpha      gl.Uniform
	// Additional uniforms
	u map[string]gl.Uniform
}

func newShaderProgram(vert, frag, id string) (s *ShaderProgram) {
	vertObj := compileShader(gl.VERTEX_SHADER, vert)
	fragObj := compileShader(gl.FRAGMENT_SHADER, frag)
	prog := linkProgram(vertObj, fragObj)

	s = &ShaderProgram{program: prog}
	s.aPos = gl.GetAttribLocation(s.program, "position")
	s.aUv = gl.GetAttribLocation(s.program, "uv")
	s.aVert = gl.GetAttribLocation(s.program, "VertCoord")

	s.uModelView = gl.GetUniformLocation(s.program, "modelview")
	s.uProjection = gl.GetUniformLocation(s.program, "projection")
	s.uTexture = gl.GetUniformLocation(s.program, "tex")
	s.uAlpha = gl.GetUniformLocation(s.program, "alpha")
	s.u = make(map[string]gl.Uniform)
	return
}

func (s *ShaderProgram) RegisterUniforms(names ...string) {
	for _, name := range names {
		s.u[name] = gl.GetUniformLocation(s.program, name)
	}
}

func (s *ShaderProgram) UseProgram() {
	gl.UseProgram(s.program)
}

func compileShader(shaderType gl.Enum, src string) (shader gl.Shader) {
	shader = gl.CreateShader(shaderType)
	gl.ShaderSource(shader, src)
	gl.CompileShader(shader)
	ok := gl.GetShaderi(shader, gl.COMPILE_STATUS)
	if ok == 0 {
		log := gl.GetShaderInfoLog(shader)
		gl.DeleteShader(shader)
		panic(Error("Shader compile error: " + log))
	}
	return
}

func linkProgram(v, f gl.Shader) (program gl.Program) {
	program = gl.CreateProgram()
	gl.AttachShader(program, v)
	gl.AttachShader(program, f)
	gl.LinkProgram(program)
	// Mark shaders for deletion when the program is deleted
	gl.DeleteShader(v)
	gl.DeleteShader(f)
	ok := gl.GetProgrami(program, gl.LINK_STATUS)
	if ok == 0 {
		log := gl.GetProgramInfoLog(program)
		gl.DeleteProgram(program)
		panic(Error("Link error: " + log))
	}
	return
}

// ------------------------------------------------------------------
// Texture

type Texture struct {
	handle gl.Texture
}

// Generate a new texture name
func newTexture() (t *Texture) {
	t = &Texture{gl.CreateTexture()}
	runtime.SetFinalizer(t, func (t *Texture) {
		sys.mainThreadTask <- func() {
			gl.DeleteTexture(t.handle)
		}
	})
	return
}

// Bind a texture and upload texel data to it
func (t *Texture) SetData(width, height, depth int32, filter bool, data []byte) {
	var interp int = gl.NEAREST
	if filter {
		interp = gl.LINEAR
	}

	var format gl.Enum = gl.LUMINANCE
	if depth == 24 {
		format = gl.RGB
	} else if depth == 32 {
		format = gl.RGBA
	}

	gl.BindTexture(gl.TEXTURE_2D, t.handle)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.TexImage2D(gl.TEXTURE_2D, 0, int(width), int(height), format, gl.UNSIGNED_BYTE, data)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, interp)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, interp)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
}

// ------------------------------------------------------------------
// Renderer

type Renderer struct {
	fbo           gl.Framebuffer
	fbo_texture   gl.Texture
	// Normal rendering
	rbo_depth     gl.Renderbuffer
	// MSAA rendering
	fbo_f         gl.Framebuffer
	fbo_f_texture *Texture
	// Post-processing shaders
	postVertBuffer gl.Buffer
	postShaderSelect []*ShaderProgram
}

//go:embed shaders/ident.vert.glsl
var identVertShader string

//go:embed shaders/ident.frag.glsl
var identFragShader string

func newRenderer() (r *Renderer) {
	sys.errLog.Printf("Using OpenGL %v (%v)",
		gl.GetString(gl.VERSION), gl.GetString(gl.RENDERER))

	r = &Renderer{}
	r.postShaderSelect = make([]*ShaderProgram, 1+len(sys.externalShaderList))

	// Data buffers for rendering
	postVertData := f32.Bytes(binary.LittleEndian, -1, -1, 1, -1, -1, 1, 1, 1)
	r.postVertBuffer = gl.CreateBuffer()
	gl.BindBuffer(gl.ARRAY_BUFFER, r.postVertBuffer)
	gl.BufferData(gl.ARRAY_BUFFER, postVertData, gl.STATIC_DRAW)

	// Compile postprocessing shaders

	// Calculate total amount of shaders loaded.
	r.postShaderSelect = make([]*ShaderProgram, 1+len(sys.externalShaderList))

	// Ident shader (no postprocessing)
	r.postShaderSelect[0] = newShaderProgram(identVertShader, identFragShader, "Identity Postprocess")
	r.postShaderSelect[0].RegisterUniforms("Texture", "TextureSize")

	// External Shaders
	for i := 0; i < len(sys.externalShaderList); i++ {
		r.postShaderSelect[1+i] = newShaderProgram(sys.externalShaders[0][i],
			sys.externalShaders[1][i], fmt.Sprintf("Postprocess Shader #%v", i+1))
		r.postShaderSelect[1+i].RegisterUniforms("Texture", "TextureSize")
	}

	if sys.multisampleAntialiasing {
		gl.Enable(gl.MULTISAMPLE)
	}

	gl.ActiveTexture(gl.TEXTURE0)
	r.fbo_texture = gl.CreateTexture()

	if sys.multisampleAntialiasing {
		gl.BindTexture(gl.TEXTURE_2D_MULTISAMPLE, r.fbo_texture)
	} else {
		gl.BindTexture(gl.TEXTURE_2D, r.fbo_texture)
	}

	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

	if sys.multisampleAntialiasing {
		gl.TexImage2DMultisample(gl.TEXTURE_2D_MULTISAMPLE, 16, gl.RGBA, int(sys.scrrect[2]), int(sys.scrrect[3]), false)
	} else {
		gl.TexImage2D(gl.TEXTURE_2D, 0, int(sys.scrrect[2]), int(sys.scrrect[3]), gl.RGBA, gl.UNSIGNED_BYTE, nil)
	}

	gl.BindTexture(gl.TEXTURE_2D, gl.NoTexture)

	if sys.multisampleAntialiasing {
		r.fbo_f_texture = newTexture()
		r.fbo_f_texture.SetData(sys.scrrect[2], sys.scrrect[3], 32, false, nil)
	} else {
		r.rbo_depth = gl.CreateRenderbuffer()
		gl.BindRenderbuffer(gl.RENDERBUFFER, r.rbo_depth)
		gl.RenderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT16, int(sys.scrrect[2]), int(sys.scrrect[3]))
		gl.BindRenderbuffer(gl.RENDERBUFFER, gl.NoRenderbuffer)
	}

	r.fbo = gl.CreateFramebuffer()
	gl.BindFramebuffer(gl.FRAMEBUFFER, r.fbo)

	if sys.multisampleAntialiasing {
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D_MULTISAMPLE, r.fbo_texture, 0)

		r.fbo_f = gl.CreateFramebuffer()
		gl.BindFramebuffer(gl.FRAMEBUFFER, r.fbo_f)
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, r.fbo_f_texture.handle, 0)
	} else {
		gl.FramebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, r.fbo_texture, 0)
		gl.FramebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, r.rbo_depth)
	}

	if status := gl.CheckFramebufferStatus(gl.FRAMEBUFFER); status != gl.FRAMEBUFFER_COMPLETE {
		sys.errLog.Printf("framebuffer create failed: 0x%x", status)
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, gl.NoFramebuffer)

	return
}

func (r *Renderer) BeginFrame() {
	gl.BindFramebuffer(gl.FRAMEBUFFER, r.fbo)
}

func (r *Renderer) EndFrame() {
	if sys.multisampleAntialiasing {
		gl.BindFramebuffer(gl.DRAW_FRAMEBUFFER, r.fbo_f)
		gl.BindFramebuffer(gl.READ_FRAMEBUFFER, r.fbo)
		gl.BlitFramebuffer(0, 0, int(sys.scrrect[2]), int(sys.scrrect[3]), 0, 0, int(sys.scrrect[2]), int(sys.scrrect[3]), gl.COLOR_BUFFER_BIT, gl.LINEAR)
	}

	gl.BindFramebuffer(gl.FRAMEBUFFER, gl.NoFramebuffer)

	postShader := r.postShaderSelect[sys.postProcessingShader]

	gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
	postShader.UseProgram()

	if sys.multisampleAntialiasing {
		gl.BindTexture(gl.TEXTURE_2D, r.fbo_f_texture.handle)
	} else {
		gl.BindTexture(gl.TEXTURE_2D, r.fbo_texture)
	}

	gl.Uniform1i(postShader.u["Texture"], 0)
	gl.Uniform2f(postShader.u["TextureSize"], float32(sys.scrrect[2]), float32(sys.scrrect[3]))

	gl.BindBuffer(gl.ARRAY_BUFFER, r.postVertBuffer)
	gl.EnableVertexAttribArray(postShader.aVert)
	gl.VertexAttribPointer(postShader.aVert, 2, gl.FLOAT, false, 0, 0)

	gl.DrawArrays(gl.TRIANGLE_STRIP, 0, 4)

	gl.DisableVertexAttribArray(postShader.aVert)
}
