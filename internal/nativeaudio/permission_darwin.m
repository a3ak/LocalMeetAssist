#import <Foundation/Foundation.h>
#import <AVFoundation/AVFoundation.h>
#import <dispatch/dispatch.h>

// lm_macos_microphone_status reports the microphone permission of this process:
// 0 — not determined, 1 — restricted, 2 — denied, 3 — authorized.
int lm_macos_microphone_status(void)
{
    return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

// lm_macos_microphone_request asks for microphone access and waits for the
// answer. It only prompts when the bundle explains the reason: a bare binary
// (go run) has no Info.plist, and requestAccess without a usage description
// terminates the process. In that case the current status is returned, so the
// capture keeps working through the terminal's own permission.
int lm_macos_microphone_request(void)
{
    int status = lm_macos_microphone_status();
    if (status != 0) return status;
    NSString* reason = [[NSBundle mainBundle] objectForInfoDictionaryKey:@"NSMicrophoneUsageDescription"];
    // -1 — «спросить нельзя»: обычный бинарник запущен из терминала, и доступ
    // определяется разрешением самого терминала.
    if (reason == nil || reason.length == 0) return -1;
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
    __block BOOL allowed = NO;
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
        allowed = granted;
        dispatch_semaphore_signal(semaphore);
    }];
    dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 60ull * NSEC_PER_SEC));
    return allowed ? 3 : 2;
}
