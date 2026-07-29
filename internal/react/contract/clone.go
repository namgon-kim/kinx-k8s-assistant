package contract

import "reflect"

// CloneDataMap deep-copies JSON-like runtime data while preserving concrete
// map and slice types used by tool providers.
func CloneDataMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := cloneDataValue(reflect.ValueOf(source))
	return cloned.Interface().(map[string]any)
}

func cloneDataValue(source reflect.Value) reflect.Value {
	if !source.IsValid() {
		return source
	}
	switch source.Kind() {
	case reflect.Interface:
		if source.IsNil() {
			return reflect.Zero(source.Type())
		}
		value := cloneDataValue(source.Elem())
		cloned := reflect.New(source.Type()).Elem()
		cloned.Set(value)
		return cloned
	case reflect.Pointer:
		if source.IsNil() {
			return reflect.Zero(source.Type())
		}
		cloned := reflect.New(source.Type().Elem())
		cloned.Elem().Set(cloneDataValue(source.Elem()))
		return cloned
	case reflect.Map:
		if source.IsNil() {
			return reflect.Zero(source.Type())
		}
		cloned := reflect.MakeMapWithSize(source.Type(), source.Len())
		iter := source.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(cloneDataValue(iter.Key()), cloneDataValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if source.IsNil() {
			return reflect.Zero(source.Type())
		}
		cloned := reflect.MakeSlice(source.Type(), source.Len(), source.Len())
		for i := 0; i < source.Len(); i++ {
			cloned.Index(i).Set(cloneDataValue(source.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(source.Type()).Elem()
		for i := 0; i < source.Len(); i++ {
			cloned.Index(i).Set(cloneDataValue(source.Index(i)))
		}
		return cloned
	case reflect.Struct:
		// Start with a value copy so unexported implementation fields remain
		// intact, then detach every exported runtime-data field recursively.
		cloned := reflect.New(source.Type()).Elem()
		cloned.Set(source)
		for i := 0; i < source.NumField(); i++ {
			if source.Type().Field(i).PkgPath != "" || !cloned.Field(i).CanSet() {
				continue
			}
			cloned.Field(i).Set(cloneDataValue(source.Field(i)))
		}
		return cloned
	default:
		return source
	}
}
