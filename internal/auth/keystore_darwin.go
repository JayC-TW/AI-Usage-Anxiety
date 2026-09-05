//go:build darwin && cgo

package auth

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static OSStatus loadKey(const char *service, const char *account, void **bytes, long *size) {
    CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    if (!s || !a) {
        if (s) CFRelease(s);
        if (a) CFRelease(a);
        return errSecParam;
    }
    const void *keys[] = {kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit};
    const void *values[] = {kSecClassGenericPassword, s, a, kCFBooleanTrue, kSecMatchLimitOne};
    CFDictionaryRef query = CFDictionaryCreate(NULL, keys, values, 5,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    if (status == errSecSuccess) {
        if (!result || CFGetTypeID(result) != CFDataGetTypeID()) { status = errSecParam; }
        else {
            *size = CFDataGetLength((CFDataRef)result);
            if (*size <= 0 || *size > 8192) { status = errSecParam; }
            else {
                *bytes = malloc(*size);
                if (!*bytes) { status = errSecAllocate; }
                else { memcpy(*bytes, CFDataGetBytePtr((CFDataRef)result), *size); }
            }
        }
    }
    if (result) CFRelease(result);
    CFRelease(query); CFRelease(a); CFRelease(s);
    return status;
}

static OSStatus saveKey(const char *service, const char *account, const void *key, long size) {
    CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
    CFDataRef data = CFDataCreate(NULL, key, size);
    if (!s || !a || !data) {
        if (s) CFRelease(s);
        if (a) CFRelease(a);
        if (data) CFRelease(data);
        return errSecParam;
    }
    const void *keys[] = {kSecClass, kSecAttrService, kSecAttrAccount};
    const void *values[] = {kSecClassGenericPassword, s, a};
    CFDictionaryRef query = CFDictionaryCreate(NULL, keys, values, 3,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    const void *attributeKeys[] = {kSecValueData};
    const void *attributeValues[] = {data};
    CFDictionaryRef attributes = CFDictionaryCreate(NULL, attributeKeys, attributeValues, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    OSStatus status = SecItemUpdate(query, attributes);
    if (status == errSecItemNotFound) {
        CFMutableDictionaryRef item = CFDictionaryCreateMutableCopy(NULL, 0, query);
        CFDictionarySetValue(item, kSecValueData, data);
        status = SecItemAdd(item, NULL);
        CFRelease(item);
    }
    CFRelease(attributes);
    CFRelease(query);
    CFRelease(data);
    CFRelease(a);
    CFRelease(s);
    return status;
}
*/
import "C"
import "unsafe"

// Keep credentials in process memory; never pass them through argv or a file.
func saveNativeKey(service, account, key string) error {
	s, a := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	bytes := []byte(key)
	status := C.saveKey(s, a, unsafe.Pointer(&bytes[0]), C.long(len(bytes)))
	for i := range bytes {
		bytes[i] = 0
	}
	if status != C.errSecSuccess {
		return ErrKeychainUnavailable
	}
	return nil
}

func loadNativeKey(service, account string) (string, error) {
	s, a := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	var data unsafe.Pointer
	var size C.long
	status := C.loadKey(s, a, &data, &size)
	if data != nil {
		defer C.free(data)
	}
	if status == C.errSecItemNotFound {
		return "", ErrNotFound
	}
	if status != C.errSecSuccess {
		return "", ErrKeychainUnavailable
	}
	return string(C.GoBytes(data, C.int(size))), nil
}
