// The native half of internal/nativeterm, built into libxxvinative.dylib with
// Ghostty's full library: Ghostty surfaces as subviews of the window's content
// view, laid over the page where the page draws a terminal's pane.
//
// What a host has to do for a surface — keys, mouse, clipboard — follows
// Ghostty's own macOS host (macos/Sources/Ghostty/Surface View). Every call is
// made on the main thread: AppKit and Ghostty's surfaces both require it.
#import <Cocoa/Cocoa.h>
#include "ghostty.h"

static ghostty_app_t app;
static NSMutableDictionary<NSString *, NSView *> *views;

static ghostty_input_mods_e modsOf(NSEventModifierFlags f) {
  int m = 0;
  if (f & NSEventModifierFlagShift) m |= GHOSTTY_MODS_SHIFT;
  if (f & NSEventModifierFlagControl) m |= GHOSTTY_MODS_CTRL;
  if (f & NSEventModifierFlagOption) m |= GHOSTTY_MODS_ALT;
  if (f & NSEventModifierFlagCommand) m |= GHOSTTY_MODS_SUPER;
  if (f & NSEventModifierFlagCapsLock) m |= GHOSTTY_MODS_CAPS;
  return (ghostty_input_mods_e)m;
}

@interface NTView : NSView
@property ghostty_surface_t surface;
@property(copy) NSString *ident;
@end

@implementation NTView {
  NSTrackingArea *tracking;
}

- (BOOL)acceptsFirstResponder { return YES; }
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return YES; }

- (BOOL)becomeFirstResponder {
  if (self.surface) ghostty_surface_set_focus(self.surface, true);
  return YES;
}

- (BOOL)resignFirstResponder {
  if (self.surface) ghostty_surface_set_focus(self.surface, false);
  return YES;
}

// ---- keys ----

- (void)key:(NSEvent *)e action:(ghostty_input_action_e)a {
  if (!self.surface) return;
  ghostty_input_key_s k = {0};
  k.action = a;
  k.mods = modsOf(e.modifierFlags);
  k.keycode = e.keyCode;
  NSString *chars = e.characters;
  if (a != GHOSTTY_ACTION_RELEASE && chars.length > 0) {
    unichar c = [chars characterAtIndex:0];
    // Control characters and function keys are the key's to say, not text's.
    if (c >= 0x20 && !(c >= 0xF700 && c <= 0xF8FF)) k.text = chars.UTF8String;
  }
  NSString *un = e.charactersIgnoringModifiers;
  if (un.length > 0) k.unshifted_codepoint = [un characterAtIndex:0];
  ghostty_surface_key(self.surface, k);
}

- (void)keyDown:(NSEvent *)e { [self key:e action:e.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS]; }
- (void)keyUp:(NSEvent *)e { [self key:e action:GHOSTTY_ACTION_RELEASE]; }

// The Edit menu's items reach the first responder by selector: with the
// terminal focused they are the terminal's copy, paste and select all.
- (void)copy:(id)sender { [self binding:@"copy_to_clipboard"]; }
- (void)paste:(id)sender { [self binding:@"paste_from_clipboard"]; }
- (void)selectAll:(id)sender { [self binding:@"select_all"]; }
- (void)binding:(NSString *)name {
  if (!self.surface) return;
  const char *s = name.UTF8String;
  ghostty_surface_binding_action(self.surface, s, strlen(s));
}

// ---- mouse ----

- (void)updateTrackingAreas {
  if (tracking) [self removeTrackingArea:tracking];
  tracking = [[NSTrackingArea alloc] initWithRect:self.bounds
      options:NSTrackingMouseMoved | NSTrackingActiveInKeyWindow | NSTrackingInVisibleRect
      owner:self userInfo:nil];
  [self addTrackingArea:tracking];
  [super updateTrackingAreas];
}

- (void)position:(NSEvent *)e {
  if (!self.surface) return;
  NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
  // Ghostty measures from the top left; AppKit from the bottom left.
  ghostty_surface_mouse_pos(self.surface, p.x, self.frame.size.height - p.y, modsOf(e.modifierFlags));
}

- (void)button:(NSEvent *)e state:(ghostty_input_mouse_state_e)st which:(ghostty_input_mouse_button_e)b {
  if (!self.surface) return;
  [self position:e];
  ghostty_surface_mouse_button(self.surface, st, b, modsOf(e.modifierFlags));
}

- (void)mouseDown:(NSEvent *)e {
  [self.window makeFirstResponder:self];
  [self button:e state:GHOSTTY_MOUSE_PRESS which:GHOSTTY_MOUSE_LEFT];
}
- (void)mouseUp:(NSEvent *)e { [self button:e state:GHOSTTY_MOUSE_RELEASE which:GHOSTTY_MOUSE_LEFT]; }
- (void)rightMouseDown:(NSEvent *)e { [self button:e state:GHOSTTY_MOUSE_PRESS which:GHOSTTY_MOUSE_RIGHT]; }
- (void)rightMouseUp:(NSEvent *)e { [self button:e state:GHOSTTY_MOUSE_RELEASE which:GHOSTTY_MOUSE_RIGHT]; }
- (void)otherMouseDown:(NSEvent *)e { [self button:e state:GHOSTTY_MOUSE_PRESS which:GHOSTTY_MOUSE_MIDDLE]; }
- (void)otherMouseUp:(NSEvent *)e { [self button:e state:GHOSTTY_MOUSE_RELEASE which:GHOSTTY_MOUSE_MIDDLE]; }
- (void)mouseMoved:(NSEvent *)e { [self position:e]; }
- (void)mouseDragged:(NSEvent *)e { [self position:e]; }
- (void)rightMouseDragged:(NSEvent *)e { [self position:e]; }
- (void)otherMouseDragged:(NSEvent *)e { [self position:e]; }

- (void)scrollWheel:(NSEvent *)e {
  if (!self.surface) return;
  double x = e.scrollingDeltaX, y = e.scrollingDeltaY;
  int mods = 0;
  if (e.hasPreciseScrollingDeltas) {
    // Ghostty's own host doubles a trackpad's deltas; it reads right.
    x *= 2; y *= 2;
    mods |= 1; // precision
  }
  // Momentum phase, packed above the precision bit as Ghostty expects.
  int momentum = 0;
  switch (e.momentumPhase) {
    case NSEventPhaseBegan: momentum = 1; break;
    case NSEventPhaseStationary: momentum = 2; break;
    case NSEventPhaseChanged: momentum = 3; break;
    case NSEventPhaseEnded: momentum = 4; break;
    case NSEventPhaseCancelled: momentum = 5; break;
    case NSEventPhaseMayBegin: momentum = 6; break;
    default: break;
  }
  mods |= momentum << 1;
  ghostty_surface_mouse_scroll(self.surface, x, y, mods);
}

// ---- size ----

- (void)setFrameSize:(NSSize)s {
  [super setFrameSize:s];
  if (!self.surface) return;
  NSSize px = [self convertSizeToBacking:s];
  ghostty_surface_set_size(self.surface, (uint32_t)px.width, (uint32_t)px.height);
}

- (void)viewDidChangeBackingProperties {
  [super viewDidChangeBackingProperties];
  if (!self.surface || !self.window) return;
  double sc = self.window.backingScaleFactor;
  ghostty_surface_set_content_scale(self.surface, sc, sc);
  [self setFrameSize:self.frame.size];
}
@end

// ---- the app's callbacks ----

static void wakeup(void *ud) {
  dispatch_async(dispatch_get_main_queue(), ^{ if (app) ghostty_app_tick(app); });
}

static bool action(ghostty_app_t a, ghostty_target_s t, ghostty_action_s act) { return false; }

// Only plain text crosses: a terminal pastes text, and asks for nothing else.
static ghostty_clipboard_read_result_e readClip(void *ud, ghostty_clipboard_e loc, void *state,
                                                const char *const *mimes, size_t n, bool list) {
  NTView *v = (__bridge NTView *)ud;
  if (!v.surface || loc != GHOSTTY_CLIPBOARD_STANDARD) return GHOSTTY_CLIPBOARD_READ_UNSUPPORTED;
  NSString *text = [NSPasteboard.generalPasteboard stringForType:NSPasteboardTypeString];
  if (!text) return GHOSTTY_CLIPBOARD_READ_UNAVAILABLE;
  const char *mime = NULL;
  for (size_t i = 0; i < n; i++) {
    if (mimes[i] && strncmp(mimes[i], "text/plain", 10) == 0) { mime = mimes[i]; break; }
  }
  if (!mime) return GHOSTTY_CLIPBOARD_READ_UNAVAILABLE;
  NSData *data = [text dataUsingEncoding:NSUTF8StringEncoding];
  ghostty_clipboard_content_s content = {mime, data.bytes, data.length};
  const char *available[] = {"text/plain"};
  ghostty_clipboard_complete_s done = {&content, 1, available, list ? 1 : 0, true, false};
  ghostty_surface_complete_clipboard_request(v.surface, &done, state);
  return GHOSTTY_CLIPBOARD_READ_STARTED;
}

static void confirmClip(void *ud, const ghostty_clipboard_confirm_s *c, void *s, ghostty_clipboard_request_e r) {}

static void writeClip(void *ud, ghostty_clipboard_e loc, const ghostty_clipboard_content_s *contents,
                      size_t n, bool confirm) {
  if (loc != GHOSTTY_CLIPBOARD_STANDARD) return;
  for (size_t i = 0; i < n; i++) {
    if (!contents[i].mime || strncmp(contents[i].mime, "text/plain", 10) != 0) continue;
    NSString *text = [[NSString alloc] initWithBytes:contents[i].data length:contents[i].len
                                            encoding:NSUTF8StringEncoding];
    if (!text) return;
    [NSPasteboard.generalPasteboard clearContents];
    [NSPasteboard.generalPasteboard setString:text forType:NSPasteboardTypeString];
    return;
  }
}

void nt_close(const char *cid);

// The bridge exited: the terminal ended or its socket closed. The page shows the
// ended terminal again once the view is gone.
static void closeSurface(void *ud, bool alive) {
  NTView *v = (__bridge NTView *)ud;
  NSString *ident = v.ident;
  dispatch_async(dispatch_get_main_queue(), ^{ nt_close(ident.UTF8String); });
}

static void onMain(dispatch_block_t b) {
  if ([NSThread isMainThread]) b(); else dispatch_sync(dispatch_get_main_queue(), b);
}

static bool ensureApp(void) {
  if (app) return true;
  static char *argv[] = {"xxvi", NULL};
  if (ghostty_init(1, argv) != 0) return false;
  ghostty_config_t cfg = ghostty_config_new();
  // The application's colours first, so a terminal without settings of its own
  // looks like part of the window (styles.css --bg, --text) rather than a grey
  // patch in it; then the person's own Ghostty settings, which win.
  NSString *base = [NSTemporaryDirectory() stringByAppendingPathComponent:@"xxvi-ghostty.conf"];
  [@"background = #111216\nforeground = #e6e8ee\nwindow-padding-x = 6\nwindow-padding-y = 4\n"
      writeToFile:base atomically:YES encoding:NSUTF8StringEncoding error:nil];
  ghostty_config_load_file(cfg, base.fileSystemRepresentation);
  ghostty_config_load_default_files(cfg);
  ghostty_config_finalize(cfg);
  ghostty_runtime_config_s rt = {0};
  rt.wakeup_cb = wakeup;
  rt.action_cb = action;
  rt.read_clipboard_cb = readClip;
  rt.confirm_read_clipboard_cb = confirmClip;
  rt.write_clipboard_cb = writeClip;
  rt.close_surface_cb = closeSurface;
  app = ghostty_app_new(&rt, cfg);
  views = [NSMutableDictionary new];

  // A key on its way to a focused terminal goes straight to it. AppKit first
  // offers every key to every view in the window as a key equivalent, and the
  // web view takes Esc there — a terminal never saw it, and Esc is how a CLI's
  // turn is broken off. Keys with ⌘ still go the usual way: they are the
  // menu's (copy, paste, quit).
  [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown | NSEventMaskKeyUp
                                        handler:^NSEvent *(NSEvent *e) {
    NSResponder *r = e.window.firstResponder;
    if (![r isKindOfClass:[NTView class]]) return e;
    if (e.modifierFlags & NSEventModifierFlagCommand) return e;
    NTView *v = (NTView *)r;
    if (e.type == NSEventTypeKeyDown) [v keyDown:e]; else [v keyUp:e];
    return nil;
  }];
  return app != NULL;
}

// ---- what internal/nativeterm calls ----

__attribute__((visibility("default")))
void nt_show(void *nswindow, const char *cid, const char *ccommand, double x, double y, double w, double h, double dpr, double font) {
  NSString *ident = [NSString stringWithUTF8String:cid];
  NSString *command = [NSString stringWithUTF8String:ccommand];
  onMain(^{
    if (!ensureApp()) return;
    NSWindow *win = (__bridge NSWindow *)nswindow;
    NSView *content = win.contentView;
    // The page measures in CSS pixels, which the page's zoom makes larger or
    // smaller than the window's points; its device pixel ratio over the
    // screen's says by how much.
    double k = win.backingScaleFactor > 0 && dpr > 0 ? dpr / win.backingScaleFactor : 1;
    double px = x * k, py = y * k, pw = w * k, ph = h * k;
    // The page measures from the top, the content view from the bottom.
    NSRect frame = NSMakeRect(px, content.bounds.size.height - py - ph, pw, ph);
    NTView *v = (NTView *)views[ident];
    if (!v) {
      v = [[NTView alloc] initWithFrame:frame];
      v.ident = ident;
      [content addSubview:v positioned:NSWindowAbove relativeTo:nil];
      ghostty_surface_config_s sc = ghostty_surface_config_new();
      sc.platform_tag = GHOSTTY_PLATFORM_MACOS;
      sc.platform.macos.nsview = (__bridge void *)v;
      sc.userdata = (__bridge void *)v;
      sc.scale_factor = win.backingScaleFactor;
      sc.command = command.UTF8String;
      // The page's own terminal size: the same pane then holds the same
      // columns whichever engine draws it.
      if (font > 0) sc.font_size = (float)font;
      v.surface = ghostty_surface_new(app, &sc);
      if (!v.surface) {
        [v removeFromSuperview];
        return;
      }
      ghostty_surface_set_content_scale(v.surface, win.backingScaleFactor, win.backingScaleFactor);
      views[ident] = v;
    }
    v.hidden = NO;
    [v setFrame:frame];
    [v setFrameSize:frame.size];
  });
}

__attribute__((visibility("default")))
void nt_hide(const char *cid) {
  NSString *ident = [NSString stringWithUTF8String:cid];
  onMain(^{
    NTView *v = (NTView *)views[ident];
    if (!v || v.hidden) return;
    // A hidden first responder still gets the keys: they would go on into a
    // terminal nobody can see.
    if (v.window.firstResponder == v) [v.window makeFirstResponder:nil];
    v.hidden = YES;
  });
}

__attribute__((visibility("default")))
void nt_close(const char *cid) {
  NSString *ident = [NSString stringWithUTF8String:cid];
  onMain(^{
    NTView *v = (NTView *)views[ident];
    if (!v) return;
    [views removeObjectForKey:ident];
    if (v.window.firstResponder == v) [v.window makeFirstResponder:nil];
    ghostty_surface_t s = v.surface;
    v.surface = NULL;
    [v removeFromSuperview];
    if (s) ghostty_surface_free(s);
  });
}

// The page that laid the views over itself is gone — reloaded — and the new one
// knows nothing of them: without this they would stay over it, drawing
// terminals nobody asked for.
__attribute__((visibility("default")))
void nt_close_all(void) {
  onMain(^{
    for (NSString *ident in views.allKeys) nt_close(ident.UTF8String);
  });
}
