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
// The terminal the page wants the keyboard in. The page may ask before the view
// is there or while it is off screen; it gets the keyboard as it appears.
static NSString *wanted;

// The page's own view: the keyboard goes back there, not to the window, which
// would hear keys and pass them to nobody.
static NSView *pageIn(NSView *v) {
  if ([v isKindOfClass:NSClassFromString(@"WKWebView")]) return v;
  for (NSView *s in v.subviews) {
    NSView *p = pageIn(s);
    if (p) return p;
  }
  return nil;
}

static void toPage(NSWindow *win) {
  if (![win.firstResponder isKindOfClass:NSClassFromString(@"NTView")]) return;
  [win makeFirstResponder:pageIn(win.contentView)];
}

// The ribbon's keys from inside a terminal: ⇧⌘ with an arrow, Home, End, F,
// N, J or Esc (frontend/src/views/ribbon.tsx). With ⌘ alone they are the
// terminal's own, and Ghostty binds none of these to anything this host does:
// its ⇧⌘↑ and ⇧⌘↓ repeat ⌘↑ and ⌘↓, and ⇧⌘F ends a search there is none of.
static BOOL forRibbon(NSEvent *e) {
  NSEventModifierFlags held = e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagShift |
                                                 NSEventModifierFlagOption | NSEventModifierFlagControl);
  if (held != (NSEventModifierFlagCommand | NSEventModifierFlagShift)) return NO;
  switch (e.keyCode) {
    case 123: case 124: case 125: case 126: // arrows
    case 115: case 119:                     // Home, End
    case 3: case 45: case 38: case 53:      // F, N, J, Esc — by place, whatever the layout
      return YES;
  }
  return NO;
}

// A key the menu has a command for.
static BOOL inMenu(NSMenu *menu, NSEvent *e) {
  NSEventModifierFlags want = e.modifierFlags & (NSEventModifierFlagCommand | NSEventModifierFlagShift |
                                                 NSEventModifierFlagOption | NSEventModifierFlagControl);
  NSString *key = e.charactersIgnoringModifiers.lowercaseString;
  for (NSMenuItem *item in menu.itemArray) {
    if (item.hasSubmenu && inMenu(item.submenu, e)) return YES;
    if (item.keyEquivalent.length == 0) continue;
    NSEventModifierFlags mods = item.keyEquivalentModifierMask;
    NSString *k = item.keyEquivalent;
    // An upper-case equivalent carries its shift in the letter.
    if (![k isEqualToString:k.lowercaseString]) mods |= NSEventModifierFlagShift;
    if ([k.lowercaseString isEqualToString:key] && mods == want) return YES;
  }
  return NO;
}

static ghostty_input_mods_e modsOf(NSEventModifierFlags f) {
  int m = 0;
  if (f & NSEventModifierFlagShift) m |= GHOSTTY_MODS_SHIFT;
  if (f & NSEventModifierFlagControl) m |= GHOSTTY_MODS_CTRL;
  if (f & NSEventModifierFlagOption) m |= GHOSTTY_MODS_ALT;
  if (f & NSEventModifierFlagCommand) m |= GHOSTTY_MODS_SUPER;
  if (f & NSEventModifierFlagCapsLock) m |= GHOSTTY_MODS_CAPS;
  return (ghostty_input_mods_e)m;
}

@interface NTView : NSView <NSTextInputClient>
@property ghostty_surface_t surface;
@property(copy) NSString *ident;
@end

// A key's own text, as Ghostty wants it: a control character is said without
// its control, which Ghostty encodes itself, and a function key has none.
static NSString *textOf(NSEvent *e) {
  NSString *chars = e.characters;
  if (chars.length != 1) return chars;
  unichar c = [chars characterAtIndex:0];
  if (c < 0x20) return [e charactersByApplyingModifiers:e.modifierFlags & ~NSEventModifierFlagControl];
  if (c >= 0xF700 && c <= 0xF8FF) return nil;
  return chars;
}

static BOOL isControl(NSString *t) {
  return t.length == 1 && [t characterAtIndex:0] < 0x20;
}

@implementation NTView {
  NSTrackingArea *tracking;
  // Text an input method is composing — a dead key's accent, a word in
  // Japanese — shown by Ghostty as preedit until it is committed.
  NSMutableAttributedString *marked;
  // What the input method committed during the keyDown under way; nil outside
  // one.
  NSMutableArray<NSString *> *committed;
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
//
// As Ghostty's own host does it (SurfaceView_AppKit.swift): the key goes
// through the input method first, and what reaches the terminal is either the
// text it committed or the key itself.

- (void)send:(NSEvent *)e action:(ghostty_input_action_e)a text:(NSString *)text composing:(BOOL)composing {
  if (!self.surface) return;
  ghostty_input_key_s k = {0};
  k.action = a;
  k.keycode = e.keyCode;
  k.mods = modsOf(e.modifierFlags);
  // Control and ⌘ never make text; whatever else is held is taken to have.
  k.consumed_mods = modsOf(e.modifierFlags & ~(NSEventModifierFlagControl | NSEventModifierFlagCommand));
  NSString *un = [e charactersByApplyingModifiers:0];
  if (un.length > 0) k.unshifted_codepoint = [un characterAtIndex:0];
  k.composing = composing;
  if (text.length > 0) k.text = text.UTF8String;
  ghostty_surface_key(self.surface, k);
}

// Text the input method committed, which is no key's.
- (void)commit:(NSString *)text action:(ghostty_input_action_e)a {
  if (!self.surface || text.length == 0) return;
  ghostty_input_key_s k = {0};
  k.action = a;
  k.text = text.UTF8String;
  ghostty_surface_key(self.surface, k);
}

- (void)keyDown:(NSEvent *)e {
  if (!self.surface) return;
  ghostty_input_action_e a = e.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS;
  BOOL markedBefore = marked.length > 0;
  committed = [NSMutableArray array];
  [self interpretKeyEvents:@[ e ]];
  NSArray<NSString *> *texts = committed;
  committed = nil;
  [self syncPreedit];
  // A key that ended a composition is the composition's, not the terminal's:
  // backspace in Japanese input cancels it rather than deleting what came before.
  BOOL composing = marked.length > 0 || markedBefore;
  if (texts.count > 0) {
    for (NSString *t in texts) {
      if (composing && isControl(t)) continue;
      if (markedBefore) [self commit:t action:a];
      else [self send:e action:a text:t composing:NO];
    }
    return;
  }
  if (composing && isControl(e.characters)) return;
  [self send:e action:a text:textOf(e) composing:composing];
}

- (void)keyUp:(NSEvent *)e { [self send:e action:GHOSTTY_ACTION_RELEASE text:nil composing:NO]; }

// A modifier on its own: Ghostty shows links under the pointer while ⌘ is held,
// and a program may ask for modifier presses.
- (void)flagsChanged:(NSEvent *)e {
  if (!self.surface || marked.length > 0) return;
  int mod;
  switch (e.keyCode) {
    case 0x39: mod = GHOSTTY_MODS_CAPS; break;
    case 0x38: case 0x3C: mod = GHOSTTY_MODS_SHIFT; break;
    case 0x3B: case 0x3E: mod = GHOSTTY_MODS_CTRL; break;
    case 0x3A: case 0x3D: mod = GHOSTTY_MODS_ALT; break;
    case 0x37: case 0x36: mod = GHOSTTY_MODS_SUPER; break;
    default: return;
  }
  ghostty_input_action_e a = GHOSTTY_ACTION_RELEASE;
  if (modsOf(e.modifierFlags) & mod) {
    // Held, but by this key or by its twin on the other side?
    NSUInteger side;
    switch (e.keyCode) {
      case 0x3C: side = NX_DEVICERSHIFTKEYMASK; break;
      case 0x3E: side = NX_DEVICERCTLKEYMASK; break;
      case 0x3D: side = NX_DEVICERALTKEYMASK; break;
      case 0x36: side = NX_DEVICERCMDKEYMASK; break;
      default: side = 0;
    }
    if (!side || (e.modifierFlags & side)) a = GHOSTTY_ACTION_PRESS;
  }
  [self send:e action:a text:nil composing:NO];
}

- (void)syncPreedit {
  if (!self.surface) return;
  if (marked.length > 0) {
    const char *s = marked.string.UTF8String;
    ghostty_surface_preedit(self.surface, s, strlen(s));
  } else {
    ghostty_surface_preedit(self.surface, NULL, 0);
  }
}

// ---- NSTextInputClient ----

- (void)insertText:(id)string replacementRange:(NSRange)range {
  NSString *text = [string isKindOfClass:[NSAttributedString class]] ? [string string] : string;
  [self unmarkText];
  if (committed) [committed addObject:text];
  // Outside a key: the input method's own window, dictation.
  else [self commit:text action:GHOSTTY_ACTION_PRESS];
}

- (void)setMarkedText:(id)string selectedRange:(NSRange)sel replacementRange:(NSRange)range {
  marked = [string isKindOfClass:[NSAttributedString class]]
      ? [[NSMutableAttributedString alloc] initWithAttributedString:string]
      : [[NSMutableAttributedString alloc] initWithString:string];
  // Changed from outside a key — the layout switched mid-composition.
  if (!committed) [self syncPreedit];
}

- (void)unmarkText {
  if (marked.length == 0) return;
  marked = nil;
  if (!committed) [self syncPreedit];
}

// Keys the input method does not turn into text arrive here as editing
// commands; the terminal gets them as keys from keyDown instead.
- (void)doCommandBySelector:(SEL)selector {}

- (BOOL)hasMarkedText { return marked.length > 0; }
- (NSRange)markedRange { return marked.length > 0 ? NSMakeRange(0, marked.length) : NSMakeRange(NSNotFound, 0); }
- (NSRange)selectedRange { return NSMakeRange(NSNotFound, 0); }
- (NSArray<NSAttributedStringKey> *)validAttributesForMarkedText { return @[]; }
- (NSAttributedString *)attributedSubstringForProposedRange:(NSRange)r actualRange:(NSRangePointer)actual { return nil; }
- (NSUInteger)characterIndexForPoint:(NSPoint)p { return 0; }

// Where the input method's window goes: at the cursor.
- (NSRect)firstRectForCharacterRange:(NSRange)r actualRange:(NSRangePointer)actual {
  double x = 0, y = 0, w = 0, h = 0;
  if (self.surface) ghostty_surface_ime_point(self.surface, &x, &y, &w, &h);
  NSRect rect = NSMakeRect(x, self.frame.size.height - y, 0, h);
  rect = [self convertRect:rect toView:nil];
  return self.window ? [self.window convertRectToScreen:rect] : rect;
}

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

// onMain runs b on the main thread without waiting for it. Never
// dispatch_sync: a Go thread blocked on the main one while another Go thread
// forks a process deadlocks the whole application — fork takes malloc's lock
// and waits for libdispatch's, the main thread holds libdispatch's and waits
// for malloc's. Nothing here needs an answer back, so nothing has to wait.
static void onMain(dispatch_block_t b) {
  if ([NSThread isMainThread]) b(); else dispatch_async(dispatch_get_main_queue(), b);
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
  // turn is broken off. Of the keys with ⌘, the ribbon's go to the page, and
  // the page gives the keyboard back to whichever terminal it lands on; the
  // menu's (copy, paste, quit) go to the menu; the rest are the terminal's —
  // ⌘← and ⌘→ to the ends of the line, ⌘⌫, ⌘K, and whatever the person bound.
  [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown | NSEventMaskKeyUp
                                        handler:^NSEvent *(NSEvent *e) {
    NSResponder *r = e.window.firstResponder;
    if (![r isKindOfClass:[NTView class]]) return e;
    NTView *v = (NTView *)r;
    if (e.type == NSEventTypeKeyUp) {
      [v keyUp:e];
      return nil;
    }
    if (e.modifierFlags & NSEventModifierFlagCommand) {
      if (forRibbon(e)) {
        toPage(e.window);
        return e;
      }
      if (inMenu(NSApp.mainMenu, e)) return e;
    }
    [v keyDown:e];
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
    BOOL appears = !v || v.hidden;
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
    if (appears && [wanted isEqualToString:ident]) [win makeFirstResponder:v];
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
    if (v.window.firstResponder == v) toPage(v.window);
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
    if (v.window.firstResponder == v) toPage(v.window);
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

// nt_focus gives the keyboard to a terminal, or back to the page for "".
__attribute__((visibility("default")))
void nt_focus(const char *cid) {
  NSString *ident = [NSString stringWithUTF8String:cid];
  onMain(^{
    wanted = ident.length ? ident : nil;
    if (!wanted) {
      for (NSWindow *w in NSApp.windows) toPage(w);
      return;
    }
    NTView *v = (NTView *)views[ident];
    if (!v || v.hidden) return;
    [v.window makeFirstResponder:v];
  });
}

// nt_init sets Ghostty up, on the main thread, before the application runs
// anything else (nativeterm.Init). Ghostty's setup calls setlocale, and
// setlocale racing a fork deadlocks the process inside libc: fork takes
// malloc's lock and waits for the locale's, setlocale holds the locale's and
// waits for malloc's. An application that starts processes all the time cannot
// call it at any other moment.
__attribute__((visibility("default")))
void nt_init(void) { ensureApp(); }
